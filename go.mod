module github.com/ifnotnil/wpool

go 1.25

// Test dependencies. They will not be pushed downstream as indirect ones.
require (
	github.com/ifnotnil/x/tst v0.0.2
	github.com/stretchr/testify v1.12.0
	go.uber.org/goleak v1.3.0
)

require (
	github.com/kr/text v0.2.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
