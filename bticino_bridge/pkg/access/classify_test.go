package access

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	const vmax = 3500 * time.Millisecond
	cases := []struct {
		name     string
		in       ClassifyInput
		wantType string
		wantConf string
	}{
		{"llamada, sin duración", ClassifyInput{HadCall: true, DurationKnown: false, VisitorMax: vmax}, TypeTelefonillo, ConfMedia},
		{"llamada, pulso corto", ClassifyInput{HadCall: true, Duration: 2 * time.Second, DurationKnown: true, VisitorMax: vmax}, TypeTelefonillo, ConfAlta},
		{"llamada, pulso largo", ClassifyInput{HadCall: true, Duration: 5 * time.Second, DurationKnown: true, VisitorMax: vmax}, TypeTelefonillo, ConfBaja},
		{"sin llamada, sin duración", ClassifyInput{HadCall: false, DurationKnown: false, VisitorMax: vmax}, TypeLlave, ConfMedia},
		{"sin llamada, pulso largo", ClassifyInput{HadCall: false, Duration: 4500 * time.Millisecond, DurationKnown: true, VisitorMax: vmax}, TypeLlave, ConfAlta},
		{"sin llamada, pulso corto (conflicto)", ClassifyInput{HadCall: false, Duration: 2 * time.Second, DurationKnown: true, VisitorMax: vmax}, TypeDesconocido, ConfBaja},
		{"borde: duración == umbral con llamada", ClassifyInput{HadCall: true, Duration: vmax, DurationKnown: true, VisitorMax: vmax}, TypeTelefonillo, ConfAlta},
		{"borde: duración == umbral sin llamada", ClassifyInput{HadCall: false, Duration: vmax, DurationKnown: true, VisitorMax: vmax}, TypeDesconocido, ConfBaja},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.in)
			if got.Type != c.wantType || got.Confidence != c.wantConf {
				t.Errorf("Classify(%+v) = {%s,%s}; quiero {%s,%s}", c.in, got.Type, got.Confidence, c.wantType, c.wantConf)
			}
		})
	}
}
