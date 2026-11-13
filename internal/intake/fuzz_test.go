package intake

import (
	"os"
	"testing"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
)

// Whatever a caller types at whatever screen, Answer never panics and either
// records a valid answer or asks again.
func FuzzAnswerNeverPanics(f *testing.F) {
	b, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		f.Fatal(err)
	}
	raw, err := os.ReadFile("../../config/gated/gazetteer.yaml")
	if err != nil {
		f.Fatal(err)
	}
	g, err := location.ParseFile(raw)
	if err != nil {
		f.Fatal(err)
	}
	m, err := NewMachine(b.Tool1, g, 7)
	if err != nil {
		f.Fatal(err)
	}
	kinds := []Kind{KindLanguage, KindDanger, KindCategory, KindAgeBand, KindSpecificDanger, KindWard, KindLandmark, KindLandmarkText, KindCallback}
	f.Add(uint8(0), "1", true)
	f.Add(uint8(7), "Mkt*1", false)
	f.Add(uint8(8), "+2560772123456", true)
	f.Fuzz(func(t *testing.T, k uint8, input string, hasPhone bool) {
		p := incident.Projection{Reference: "1234567894", Language: "lg", Danger: "no", Category: "acute_medical",
			AgeBand: "under_5", SpecificDanger: "no", Ward: "w01", LastScreen: 6}
		out, err := m.Answer(p, kinds[int(k)%len(kinds)], input, hasPhone)
		if err != nil {
			return
		}
		if out.Answer != nil {
			if _, err := out.Answer.Payload(); err != nil && out.Answer.Value != incident.CallbackProvided && out.Answer.Value != incident.LandmarkOther {
				t.Fatalf("answer %+v does not encode: %v", out.Answer, err)
			}
		}
	})
}
