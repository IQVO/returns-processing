package repocontract

// Fixture values shared by the builders and the contract suites: a builder
// that does not care about an sku, site or rma uses these so every call site
// stays short; a test that cares passes its own value.
const (
	fixtureRMA   = "rma-1"
	fixtureSKU   = "sku-1"
	fixtureSite  = "SITE1"
	fixtureClass = "class:Standard"
)
