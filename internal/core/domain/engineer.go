package domain

// Engineer is an allowlisted caller.
type Engineer struct {
	ID          int64
	PhoneNumber string // E.164, e.g. +15555550123
}

// MaskPhone returns "***" followed by the last four characters, for logging.
// Strings of four characters or fewer are fully masked as "***".
func MaskPhone(phone string) string {
	if len(phone) <= 4 {
		return "***"
	}
	return "***" + phone[len(phone)-4:]
}
