package qr

import (
	"strings"
	"testing"
)

func TestGenerateSVG(t *testing.T) {
	svg, err := GenerateSVG("http://192.168.1.100:8080", 300)
	if err != nil {
		t.Fatalf("GenerateSVG failed: %v", err)
	}

	if !strings.HasPrefix(svg, `<svg xmlns="http://www.w3.org/2000/svg"`) {
		t.Errorf("SVG does not start with expected svg tag, got: %s", svg[:50])
	}

	if !strings.HasSuffix(svg, "</svg>") {
		t.Errorf("SVG does not end with </svg>")
	}

	if !strings.Contains(svg, `width="300" height="300"`) {
		t.Errorf("SVG width/height not set correctly")
	}

	if !strings.Contains(svg, `<rect x=`) {
		t.Errorf("SVG should contain rect modules")
	}
}
