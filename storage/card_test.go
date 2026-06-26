package storage

// card_test.go —— 储值 / 次卡模块单测（v4.15）
//
// 覆盖：
//   - 储值卡：售卡、扣减、2000送200 计算、扣成 0 自动 depleted、防负数、跨店隔离
//   - 次卡：售卡、扣次、自动 depleted、跨店隔离、service 不存在
//   - 调账：reason 必填、调增调减、调减防负数、流水记录完整
//   - 卡产品：CRUD、archive 时 active 卡拦截、跨店隔离、valid_days=0 = 永久
//   - 流水：每条操作都有一条对应记录

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// helpers

func makeShopForCard(t *testing.T, id string) *Shop {
	t.Helper()
	s := &Shop{
		ID:        id,
		Name:      "shop-" + id,
		Timezone:  "Asia/Shanghai",
		OpenHour:  9,
		CloseHour: 18,
		Plan:      PlanPro, // 配合 FeatureCardManagement 测试
		ExpiresAt: time.Now().AddDate(1, 0, 0),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := DB.Create(s).Error; err != nil {
		t.Fatalf("create shop: %v", err)
	}
	return s
}

func makeServiceForCard(t *testing.T, shopID, name string) *Service {
	t.Helper()
	s := &Service{
		ID:           uuid.NewString(),
		ShopID:       shopID,
		Name:         name,
		EstimatedMin: 30,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := DB.Create(s).Error; err != nil {
		t.Fatalf("create service: %v", err)
	}
	return s
}

func makeStoredValueCard(t *testing.T, shopID, name string, priceCents, face, bonus int) *Card {
	t.Helper()
	c, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:         shopID,
		Name:           name,
		Type:           CardTypeStoredValue,
		PriceCents:     priceCents,
		FaceValueCents: face,
		BonusCents:     bonus,
		ValidDays:      365,
	})
	if err != nil {
		t.Fatalf("create stored value card: %v", err)
	}
	return c
}

func makeCountCard(t *testing.T, shopID, name, serviceID, serviceName string, priceCents, totalCount int) *Card {
	t.Helper()
	c, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:      shopID,
		Name:        name,
		Type:        CardTypeCount,
		PriceCents:  priceCents,
		ServiceID:   serviceID,
		ServiceName: serviceName,
		TotalCount:  totalCount,
		ValidDays:   365,
	})
	if err != nil {
		t.Fatalf("create count card: %v", err)
	}
	return c
}

// ---- Card 产品 CRUD ----

func TestCreateCard_StoredValue_OK(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	c, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:         shop.ID,
		Name:           " 2000送200 储值卡 ", // 测试 trim
		Type:           CardTypeStoredValue,
		PriceCents:     200000,
		FaceValueCents: 200000,
		BonusCents:     20000,
		ValidDays:      365,
		Note:           "  充值 2000 送 200 ",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Name != "2000送200 储值卡" {
		t.Errorf("name 应被 trim，得到 %q", c.Name)
	}
	if c.Note != "充值 2000 送 200" {
		t.Errorf("note 应被 trim，得到 %q", c.Note)
	}
	if c.Type != CardTypeStoredValue {
		t.Errorf("type 应为 stored_value，得到 %s", c.Type)
	}
	if c.Status != CardStatusActive {
		t.Errorf("默认 status 应为 active，得到 %s", c.Status)
	}
}

func TestCreateCard_StoredValue_RejectsPriceOverTotal(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	// 实付 2200，但只到账 2000（面值 2000 + 赠送 0）—— 反向收费，禁
	_, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:         shop.ID,
		Name:           "反向收费卡",
		Type:           CardTypeStoredValue,
		PriceCents:     220000,
		FaceValueCents: 200000,
		BonusCents:     0,
	})
	if err == nil {
		t.Fatal("应拒绝（实付 > 总到账）")
	}
	if !strings.Contains(err.Error(), "实付") {
		t.Errorf("错误信息应提到「实付」，实际：%v", err)
	}
}

func TestCreateCard_Count_RequiresService(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	// 不传 service_id 应拒
	_, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:     shop.ID,
		Name:       "无 service 次卡",
		Type:       CardTypeCount,
		PriceCents: 50000,
		TotalCount: 10,
	})
	if err == nil {
		t.Fatal("次卡必须关联 service_id，应报错")
	}
}

func TestCreateCard_Count_ServiceMustBelongToSameShop(t *testing.T) {
	SetupTestDB(t)
	shop1 := makeShopForCard(t, "shop-1")
	shop2 := makeShopForCard(t, "shop-2")
	svc := makeServiceForCard(t, shop2.ID, "别店服务")
	// 把 shop2 的 service 挂到 shop1 的次卡上 —— 应拒
	_, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:      shop1.ID,
		Name:        "挂别店 service",
		Type:        CardTypeCount,
		PriceCents:  50000,
		ServiceID:   svc.ID,
		TotalCount:  10,
	})
	if err == nil {
		t.Fatal("service 必须属于本店，应报错")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误信息应提示 service 不存在，实际：%v", err)
	}
}

func TestCreateCard_DuplicateName(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	makeStoredValueCard(t, shop.ID, "重复名", 1000, 1000, 0)
	_, err := CreateCard(context.Background(), CreateCardParams{
		ShopID:         shop.ID,
		Name:           "重复名",
		Type:           CardTypeStoredValue,
		PriceCents:     2000,
		FaceValueCents: 2000,
	})
	if !errors.Is(err, ErrCardNameTaken) {
		t.Errorf("应返 ErrCardNameTaken，实际：%v", err)
	}
}

func TestArchiveCard_BlocksIfActiveCustomerCards(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "测试卡", 1000, 1000, 0)
	// 先售一张卡
	_, err := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "admin",
	})
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	// 尝试下架卡产品 —— 应被拒
	err = ArchiveCard(context.Background(), shop.ID, card.ID)
	if !errors.Is(err, ErrCardHasActiveInstances) {
		t.Errorf("应返 ErrCardHasActiveInstances，实际：%v", err)
	}
	// 把顾客卡扣成 0 → depleted → 再下架应该通过
	ccs, _ := ListCustomerCards(context.Background(), shop.ID, cust.ID, "")
	cc := ccs[0]
	_, _ = ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID:         shop.ID,
		CustomerCardID: cc.ID,
		AmountCents:    1000,
		Reason:         "测扣完",
		OperatorID:     1, OperatorName: "admin",
	})
	err = ArchiveCard(context.Background(), shop.ID, card.ID)
	if err != nil {
		t.Errorf("扣完后应能下架，实际：%v", err)
	}
}

func TestListCardsByShop_ShopIsolation(t *testing.T) {
	SetupTestDB(t)
	shop1 := makeShopForCard(t, "shop-1")
	shop2 := makeShopForCard(t, "shop-2")
	makeStoredValueCard(t, shop1.ID, "shop1 卡", 1000, 1000, 0)
	makeStoredValueCard(t, shop2.ID, "shop2 卡", 2000, 2000, 0)

	cards1, _ := ListCardsByShop(context.Background(), shop1.ID, false)
	if len(cards1) != 1 || cards1[0].Name != "shop1 卡" {
		t.Errorf("shop1 应只看到自己的卡，实际：%+v", cards1)
	}
	cards2, _ := ListCardsByShop(context.Background(), shop2.ID, false)
	if len(cards2) != 1 || cards2[0].Name != "shop2 卡" {
		t.Errorf("shop2 应只看到自己的卡，实际：%+v", cards2)
	}
}

// ---- 售卡 ----

func TestSellStoredValueCard_Bonus2000(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	// 经典 2000送200
	card := makeStoredValueCard(t, shop.ID, "2000送200", 200000, 200000, 20000)
	cc, err := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "owner",
	})
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	// 初始余额 = 面值 + 赠送 = 2200 元 = 220000 分
	if cc.BalanceCents != 220000 {
		t.Errorf("初始余额应为 220000（2000+200），得到 %d", cc.BalanceCents)
	}
	if cc.InitialBalanceCents != 220000 {
		t.Errorf("InitialBalanceCents 应记录 220000，得到 %d", cc.InitialBalanceCents)
	}
	if cc.Status != CustomerCardStatusActive {
		t.Errorf("状态应为 active，得到 %s", cc.Status)
	}
	if cc.ExpiresAt == nil {
		t.Errorf("valid_days=365 时 expires_at 不应为 nil")
	}

	// 流水应有 1 条 recharge
	txs, _ := ListCardTransactions(context.Background(), shop.ID, cc.ID, 10)
	if len(txs) != 1 {
		t.Fatalf("应有 1 条流水，实际 %d", len(txs))
	}
	if txs[0].Type != CardTxRecharge {
		t.Errorf("流水类型应为 recharge，得到 %s", txs[0].Type)
	}
	if txs[0].Delta != 220000 {
		t.Errorf("流水 delta 应为 220000，得到 %d", txs[0].Delta)
	}
	if txs[0].BalanceAfter != 220000 {
		t.Errorf("流水 balance_after 应为 220000，得到 %d", txs[0].BalanceAfter)
	}
}

func TestSellCountCard_InitialCount(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	svc := makeServiceForCard(t, shop.ID, "剪发")
	cust := MakeCustomer(t, "李四", 0, 0)
	card := makeCountCard(t, shop.ID, "10次剪发卡", svc.ID, svc.Name, 80000, 10)
	cc, err := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "owner",
	})
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	if cc.RemainingCount != 10 {
		t.Errorf("初始次数应为 10，得到 %d", cc.RemainingCount)
	}
	if cc.InitialCount != 10 {
		t.Errorf("InitialCount 应为 10，得到 %d", cc.InitialCount)
	}
	if cc.ServiceID != svc.ID {
		t.Errorf("ServiceID 应关联 service，得到 %s", cc.ServiceID)
	}
}

// ---- 扣减 ----

func TestConsumeStoredValue_DeductsAndAutoDepleted(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "1000储值", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})

	// 扣 300
	cc2, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 30000,
		Reason: "剪发", OperatorID: 1, OperatorName: "o",
	})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if cc2.BalanceCents != 70000 {
		t.Errorf("扣后余额应为 70000，得到 %d", cc2.BalanceCents)
	}
	if cc2.Status != CustomerCardStatusActive {
		t.Errorf("扣后状态应仍为 active，得到 %s", cc2.Status)
	}

	// 再扣 700 全部用完 → 自动 depleted
	cc3, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 70000,
		Reason: "染发", OperatorID: 1, OperatorName: "o",
	})
	if err != nil {
		t.Fatalf("consume 2: %v", err)
	}
	if cc3.BalanceCents != 0 {
		t.Errorf("扣完余额应为 0，得到 %d", cc3.BalanceCents)
	}
	if cc3.Status != CustomerCardStatusDepleted {
		t.Errorf("余额 0 应自动 depleted，得到 %s", cc3.Status)
	}

	// 流水应有 3 条（recharge + consume x2）
	txs, _ := ListCardTransactions(context.Background(), shop.ID, cc.ID, 10)
	if len(txs) != 3 {
		t.Fatalf("应有 3 条流水，实际 %d", len(txs))
	}
}

func TestConsumeStoredValue_RejectsOverdraw(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "1000储值", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	// 尝试扣 1500 > 余额 1000
	_, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 150000,
		Reason: "超扣测试", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("应返 ErrInsufficientBalance，实际：%v", err)
	}
	// 余额应不变
	ccAfter, _ := GetCustomerCardInShop(context.Background(), shop.ID, cc.ID)
	if ccAfter.BalanceCents != 100000 {
		t.Errorf("超扣失败后余额应不变，得到 %d", ccAfter.BalanceCents)
	}
}

func TestConsumeCount_DeductsOne(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	svc := makeServiceForCard(t, shop.ID, "剪发")
	cust := MakeCustomer(t, "李四", 0, 0)
	card := makeCountCard(t, shop.ID, "3次卡", svc.ID, svc.Name, 30000, 3)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	// 次卡忽略 amount_cents，永远扣 1 次
	_, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 999999, // 应被忽略
		Reason: "剪发", OperatorID: 1, OperatorName: "o",
	})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	cc2, _ := GetCustomerCardInShop(context.Background(), shop.ID, cc.ID)
	if cc2.RemainingCount != 2 {
		t.Errorf("次卡应扣 1 次，得到 remaining=%d", cc2.RemainingCount)
	}
}

func TestConsume_RejectsNonActive(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "1000储值", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	// 扣成 depleted
	_, _ = ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 100000,
		Reason: "全扣", OperatorID: 1, OperatorName: "o",
	})
	// 再扣 → 应被拒
	_, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 1,
		Reason: "再扣", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrCustomerCardNotActive) {
		t.Errorf("depleted 卡再扣应被拒（ErrCustomerCardNotActive），实际：%v", err)
	}
}

// ---- 调账（追溯重点）----

func TestAdjust_ReasonRequired(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "1000储值", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})

	// reason 空 → 应被拒（追溯要求）
	_, err := AdjustCustomerCard(context.Background(), AdjustCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID,
		Direction: AdjustUp, AmountCents: 100,
		Reason: "", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrReasonRequired) {
		t.Errorf("空 reason 应返 ErrReasonRequired，实际：%v", err)
	}
	// 全空白也应拒
	_, err = AdjustCustomerCard(context.Background(), AdjustCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID,
		Direction: AdjustUp, AmountCents: 100,
		Reason: "   ", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrReasonRequired) {
		t.Errorf("全空白 reason 应返 ErrReasonRequired，实际：%v", err)
	}
}

func TestAdjust_UpAddsBalanceAndLogsTransaction(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "1000储值", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})

	cc2, err := AdjustCustomerCard(context.Background(), AdjustCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID,
		Direction: AdjustUp, AmountCents: 5000,
		Reason: "补偿：上次数错", OperatorID: 7, OperatorName: "boss",
	})
	if err != nil {
		t.Fatalf("adjust up: %v", err)
	}
	if cc2.BalanceCents != 105000 {
		t.Errorf("调增后余额应为 105000，得到 %d", cc2.BalanceCents)
	}
	// 流水应有 2 条（recharge + adjust_up）
	txs, _ := ListCardTransactions(context.Background(), shop.ID, cc.ID, 10)
	if len(txs) != 2 {
		t.Fatalf("应有 2 条流水，实际 %d", len(txs))
	}
	last := txs[0] // DESC 排序，最近的在最前
	if last.Type != CardTxAdjustUp {
		t.Errorf("最近流水应为 adjust_up，得到 %s", last.Type)
	}
	if last.Delta != 5000 {
		t.Errorf("delta 应为 +5000，得到 %d", last.Delta)
	}
	if last.BalanceAfter != 105000 {
		t.Errorf("balance_after 应为 105000，得到 %d", last.BalanceAfter)
	}
	if last.Reason != "补偿：上次数错" {
		t.Errorf("reason 应被记录，实际 %q", last.Reason)
	}
	if last.OperatorName != "boss" {
		t.Errorf("operator_name 应为 boss，得到 %s", last.OperatorName)
	}
}

func TestAdjust_DownRejectsOverdraw(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "1000储值", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	// 调减 1500 > 余额 1000 → 应拒
	_, err := AdjustCustomerCard(context.Background(), AdjustCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID,
		Direction: AdjustDown, AmountCents: 150000,
		Reason: "数据修正", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("应返 ErrInsufficientBalance，实际：%v", err)
	}
}

// ---- 跨店隔离 ----

func TestCustomerCard_CrossShopIsolation(t *testing.T) {
	SetupTestDB(t)
	shop1 := makeShopForCard(t, "shop-1")
	shop2 := makeShopForCard(t, "shop-2")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop1.ID, "shop1 卡", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop1.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})

	// shop2 用 cc.ID 调扣减 → 应找不到（跨店隔离）
	_, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop2.ID, CustomerCardID: cc.ID, AmountCents: 1000,
		Reason: "跨店测试", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrCustomerCardNotFoundInShop) {
		t.Errorf("跨店应返 ErrCustomerCardNotFoundInShop，实际：%v", err)
	}

	// shop2 调账也找不到
	_, err = AdjustCustomerCard(context.Background(), AdjustCustomerCardParams{
		ShopID: shop2.ID, CustomerCardID: cc.ID,
		Direction: AdjustUp, AmountCents: 100,
		Reason: "跨店测试", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrCustomerCardNotFoundInShop) {
		t.Errorf("跨店调账应返 ErrCustomerCardNotFoundInShop，实际：%v", err)
	}
}

// ---- 永久卡 ----

func TestCreateCard_ValidDaysZero_IsPermanent(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "永久卡", 100000, 100000, 0)
	// 重新建一张 valid_days=0 的卡
	card2, _ := CreateCard(context.Background(), CreateCardParams{
		ShopID: shop.ID, Name: "真·永久卡", Type: CardTypeStoredValue,
		PriceCents: 100000, FaceValueCents: 100000, BonusCents: 0,
		ValidDays: 0,
	})
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card2.ID,
		OperatorID: 1, OperatorName: "o",
	})
	if cc.ExpiresAt != nil {
		t.Errorf("valid_days=0 时 ExpiresAt 应为 nil，得到 %v", cc.ExpiresAt)
	}
	_ = card // 避免 unused
}

// ---- Lazy expired ----

func TestConsume_RejectsExpiredCard(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "短卡", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	// 手动把 expires_at 改成过去
	pastTime := time.Now().Add(-time.Hour)
	if err := DB.Model(cc).Update("expires_at", pastTime).Error; err != nil {
		t.Fatalf("set expired: %v", err)
	}
	_, err := ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc.ID, AmountCents: 1000,
		Reason: "过期后扣", OperatorID: 1, OperatorName: "o",
	})
	if !errors.Is(err, ErrCustomerCardExpired) {
		t.Errorf("过期卡扣减应返 ErrCustomerCardExpired，实际：%v", err)
	}
}

func TestRefreshCustomerCardExpiry(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "短卡", 100000, 100000, 0)
	cc, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	// 改 expires_at 为过去
	if err := DB.Model(cc).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("set expired: %v", err)
	}
	n, err := RefreshCustomerCardExpiry(context.Background(), shop.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n != 1 {
		t.Errorf("应标记 1 张卡为 expired，得到 %d", n)
	}
	cc2, _ := GetCustomerCardInShop(context.Background(), shop.ID, cc.ID)
	if cc2.Status != CustomerCardStatusExpired {
		t.Errorf("状态应为 expired，得到 %s", cc2.Status)
	}
}

// ---- ListCustomerCards ----

func TestListCustomerCards_ActiveFirst(t *testing.T) {
	SetupTestDB(t)
	shop := makeShopForCard(t, "shop-1")
	cust := MakeCustomer(t, "张三", 0, 0)
	card := makeStoredValueCard(t, shop.ID, "测试卡", 100000, 100000, 0)

	// 售 2 张：先售一张扣完变 depleted，再售一张 active
	cc1, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})
	_, _ = ConsumeCustomerCard(context.Background(), ConsumeCustomerCardParams{
		ShopID: shop.ID, CustomerCardID: cc1.ID, AmountCents: 100000,
		Reason: "用完", OperatorID: 1, OperatorName: "o",
	})
	cc2, _ := SellCardToCustomer(context.Background(), SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: card.ID,
		OperatorID: 1, OperatorName: "o",
	})

	cards, _ := ListCustomerCards(context.Background(), shop.ID, cust.ID, "")
	if len(cards) != 2 {
		t.Fatalf("应有 2 张卡，得到 %d", len(cards))
	}
	if cards[0].Status != CustomerCardStatusActive {
		t.Errorf("第一张应为 active（active 优先），得到 %s", cards[0].Status)
	}
	if cards[1].Status != CustomerCardStatusDepleted {
		t.Errorf("第二张应为 depleted，得到 %s", cards[1].Status)
	}
	// 检查 ID 是 cc2 在前
	if cards[0].ID != cc2.ID {
		t.Errorf("active 卡应排在前面")
	}
}