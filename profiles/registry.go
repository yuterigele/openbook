// Package profiles 提供内置参考 Profile 的显式注册入口。
package profiles

import (
	"github.com/yuterigele/openbook/profiles/beauty"
	"github.com/yuterigele/openbook/profiles/fitness_coach"
	"github.com/yuterigele/openbook/profiles/hair"
	"github.com/yuterigele/openbook/profiles/nail"
	"github.com/yuterigele/openbook/sdk/profile"
)

// NewReferenceRegistry 创建包含 Hair 和 Beauty 的参考 Profile 注册表。
func NewReferenceRegistry() (*profile.Registry, error) {
	registry := profile.NewRegistry()
	for _, definition := range []profile.Definition{hair.Definition(), beauty.Definition(), nail.Definition(), fitness_coach.Definition()} {
		if err := registry.Register(definition); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
