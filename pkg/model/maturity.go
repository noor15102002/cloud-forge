package model

// Maturity is recorded in the existing versioned limitations contract. It is
// independent of whether a capability is schedulable or its eventual verdict.
const (
	CoreMaturity         = "Product maturity: core; included in the bounded HTTP/Redis release contract, subject to exact-candidate qualification."
	ExperimentalMaturity = "Product maturity: experimental; outside the bounded HTTP/Redis release contract."
	UnsupportedMaturity  = "Product maturity: unsupported; no runtime release claim."
)

// RecordedMaturity reads only explicit report metadata. Historical reports do
// not acquire today's maturity claims when loaded by a newer renderer.
func RecordedMaturity(capability Capability) string {
	result := "not_recorded"
	for _, limit := range capability.Limitations {
		value := ""
		switch limit {
		case CoreMaturity:
			value = "core"
		case ExperimentalMaturity:
			value = "experimental"
		case UnsupportedMaturity:
			value = "unsupported"
		}
		if value != "" {
			if result != "not_recorded" && result != value {
				return "not_recorded"
			}
			result = value
		}
	}
	return result
}
