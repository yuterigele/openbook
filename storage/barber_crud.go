package storage

// barber_crud.go
//
// 理发师 CRUD（P5 商户后台管理理发师）
//
// 与现有 leave 流程的关系：
//   - BarberLeave 引用 Barber.ID，外键关联（DB 层未做 FK，靠应用层）
//   - SoftDeleteBarber 只把 active 置 false，不删行；因为：
//     * 已存在的预约 / 历史 leave / 经营报表都要回看理发师
//     * 顾客对话里的 "Tony 师傅" 指代需要历史可追溯
//   - 同名 barber 全局唯一（Barber.Name 是 uniqueIndex）：
//     即使 inactive 状态的同名 barber 也不允许被同店新建
//     提示：想恢复 → 用 ActivateBarber
//
// 多店隔离：
//   - 所有 handler 必须传 shopID，避免 A 店删 B 店的理发师
//   - 内部调用方（如 ListBarberLeavesInRange）已带 barberID，无需再校 shopID

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrBarberNameTaken 同店已存在同名 barber（active 或 inactive 都算）
//
// 注意：Barber.Name 是全局 uniqueIndex，所以"另一家店同名"也会撞 unique index。
// 这种情况下 GORM 会返回底层 UNIQUE 错误，本函数包装成 ErrBarberNameTaken 便于上层判断。
var ErrBarberNameTaken = errors.New("barber name already taken")

// ErrBarberNotFoundInShop 在指定店铺中未找到该 barber
var ErrBarberNotFoundInShop = errors.New("barber not found in this shop")

// CreateBarber 创建理发师（同店 + 全局都不可重名）
//
// 入参：
//   - shopID : 必填
//   - name   : 必填，会 trim
//   - skills : 选填，逗号分隔，例如 "剪发,染发"
//
// 返回：
//   - 创建好的 *Barber（含自动生成的 ID）
//   - ErrBarberNameTaken 如果重名
//   - 其他错误为 DB 异常
//
// 边界：
//   - name 长度 1-32，trim 后空字符串报错
//   - skills 长度上限 256
func CreateBarber(ctx context.Context, shopID, name, skills string) (*Barber, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	trimmed := trimBarberName(name)
	if trimmed == "" {
		return nil, errors.New("理发师姓名不能为空")
	}
	if len(trimmed) > 32 {
		return nil, fmt.Errorf("理发师姓名过长（最多 32 字），得到 %d 字", len(trimmed))
	}
	if len(skills) > 256 {
		return nil, fmt.Errorf("技能描述过长（最多 256 字），得到 %d 字", len(skills))
	}

	// 预检查：先查一下同店是否已有同名（不论 active/inactive）
	// 这样能给出更友好的错误，而不是只靠 unique index 兜底
	var existing Barber
	if err := DB.WithContext(ctx).
		Where("name = ?", trimmed).
		First(&existing).Error; err == nil {
		return nil, fmt.Errorf("%w：%s", ErrBarberNameTaken, trimmed)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	now := time.Now()
	b := &Barber{
		ID:        uuid.NewString(),
		ShopID:    shopID,
		Name:      trimmed,
		Skills:    skills,
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := DB.WithContext(ctx).Create(b).Error; err != nil {
		// 兜底：unique index 冲突（理论上前置检查已经拦住；这里是 race condition 兜底）
		if isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("%w：%s", ErrBarberNameTaken, trimmed)
		}
		return nil, err
	}
	return b, nil
}

// GetBarberInShop 按 ID 查 barber，校验属于指定 shop（多店隔离）
func GetBarberInShop(ctx context.Context, shopID, barberID string) (*Barber, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" || barberID == "" {
		return nil, errors.New("shop_id 和 barber_id 必填")
	}
	var b Barber
	if err := DB.WithContext(ctx).Where("id = ?", barberID).First(&b).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBarberNotFoundInShop
		}
		return nil, err
	}
	if b.ShopID != shopID {
		// 多店隔离：不告诉前端"该 ID 存在但是别家的"
		return nil, ErrBarberNotFoundInShop
	}
	return &b, nil
}

// ListAllBarbersByShop 列某店全部 barber（默认只 active，includeInactive=true 含历史）
//
// 用于商户后台"理发师管理"表格。
func ListAllBarbersByShop(ctx context.Context, shopID string, includeInactive bool) ([]Barber, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	q := DB.WithContext(ctx).Where("shop_id = ?", shopID)
	if !includeInactive {
		q = q.Where("active = ?", true)
	}
	var out []Barber
	if err := q.Order("active desc, name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// SoftDeleteBarber 软删除（active=false）
//
// 限制：
//   - 不能删除一个"还有未来 active 预约"的 barber（避免顾客来了没人服务）
//   - 已经过时间的预约不算（只查 future active）
//
// 如果要强制删除（先取消未来预约），调用方需要走 leave 流程或手动取消。
func SoftDeleteBarber(ctx context.Context, shopID, barberID string) error {
	if DB == nil {
		return errors.New("DB 未初始化")
	}
	b, err := GetBarberInShop(ctx, shopID, barberID)
	if err != nil {
		return err
	}
	if !b.Active {
		// 幂等：已经是 inactive 状态也算成功
		return nil
	}
	// 检查是否有未来 active 预约
	hasFuture, err := hasFutureActiveAppointments(ctx, b.ID)
	if err != nil {
		return err
	}
	if hasFuture {
		return fmt.Errorf("理发师 %s 还有未来的预约，请先取消或改派后再删除", b.Name)
	}
	return DB.WithContext(ctx).Model(&Barber{}).
		Where("id = ?", b.ID).
		Updates(map[string]interface{}{
			"active":     false,
			"updated_at": time.Now(),
		}).Error
}

// ActivateBarber 重新激活一个 inactive 的 barber
//
// 用于"恢复误删"或"假期结束"。同店同名约束自然满足（因为这本来就是同一个 barber）。
func ActivateBarber(ctx context.Context, shopID, barberID string) error {
	if DB == nil {
		return errors.New("DB 未初始化")
	}
	b, err := GetBarberInShop(ctx, shopID, barberID)
	if err != nil {
		return err
	}
	if b.Active {
		// 幂等
		return nil
	}
	return DB.WithContext(ctx).Model(&Barber{}).
		Where("id = ?", b.ID).
		Updates(map[string]interface{}{
			"active":     true,
			"updated_at": time.Now(),
		}).Error
}

// UpdateBarberSkills 修改技能标签
//
// 单独抽出 endpoint（暂未在 handler 暴露，预留）。
func UpdateBarberSkills(ctx context.Context, shopID, barberID, skills string) error {
	if DB == nil {
		return errors.New("DB 未初始化")
	}
	if len(skills) > 256 {
		return fmt.Errorf("技能描述过长（最多 256 字），得到 %d 字", len(skills))
	}
	b, err := GetBarberInShop(ctx, shopID, barberID)
	if err != nil {
		return err
	}
	return DB.WithContext(ctx).Model(&Barber{}).
		Where("id = ?", b.ID).
		Updates(map[string]interface{}{
			"skills":     skills,
			"updated_at": time.Now(),
		}).Error
}

// ---- helpers ----

// trimBarberName 去除前后空白（中文/英文空格都处理）
func trimBarberName(s string) string {
	// strings.TrimSpace 已经能处理 unicode 空白
	return strings.TrimSpace(s)
}

// hasFutureActiveAppointments 检查 barber 是否有未来 active 预约
//
// 时区：与 Appointment.Date/Time 保持一致，用 Asia/Shanghai。
func hasFutureActiveAppointments(ctx context.Context, barberID string) (bool, error) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	nowHHMM := now.Format("15:04")

	// 简化为：今天或之后的日期 + 状态 active
	// 性能：用 date >= today 即可覆盖；具体时段在内存里再精筛也行
	//   这里为了简单，DB 端只查 date >= today 的所有 active 预约（量小，OK）
	var appts []Appointment
	if err := DB.WithContext(ctx).
		Where("barber_id = ? AND status = ? AND date >= ?", barberID, "active", today).
		Find(&appts).Error; err != nil {
		return false, err
	}
	for _, a := range appts {
		if a.Date == today {
			// 同一天还要看 time
			if a.Time > nowHHMM {
				return true, nil
			}
		} else {
			// 未来的日期一定有未来预约
			return true, nil
		}
	}
	return false, nil
}

// isUniqueConstraintErr 判断是否为 SQLite/MySQL 的 UNIQUE 约束错误
//
// GORM v2 没有标准 API 判断这个，不同驱动错误字符串不一样。
// 简单做法：包含 "UNIQUE" 或 "Duplicate"。
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") ||
		strings.Contains(msg, "Duplicate") ||
		strings.Contains(msg, "duplicate")
}