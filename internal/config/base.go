package config

import "github.com/lonegunmanb/golden"

func NewBaseConfig(arguments golden.NewBaseConfigArgs) *golden.BaseConfig {
	arguments.DslFullName = "r42"
	arguments.DslAbbreviation = "r42"
	base := golden.NewBasicConfigFromArgs(arguments)
	base.OverrideFunctions = Functions()
	return base
}
