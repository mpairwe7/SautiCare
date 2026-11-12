package config

// gsm7Basic is the GSM 03.38 default alphabet (basic table only). Characters
// from the extension table ({ } [ ] ~ \ | ^ €) take two septets and are not
// rendered alike by every network, so caller-facing USSD text uses the basic
// table only and the length gate counts one character per septet.
const gsm7Basic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

var gsm7Set = func() map[rune]bool {
	m := map[rune]bool{}
	for _, r := range gsm7Basic {
		m[r] = true
	}
	return m
}()

// IsGSM7 reports whether every character of s is in the GSM 03.38 basic
// table.
func IsGSM7(s string) bool {
	for _, r := range s {
		if !gsm7Set[r] {
			return false
		}
	}
	return true
}
