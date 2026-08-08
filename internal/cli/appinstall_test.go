package cli

import "testing"

func TestBundleRootFromExecutable(t *testing.T) {
	got, err := bundleRootFromExecutable("/Applications/Bx.app/Contents/Resources/bx-cli")
	if err != nil || got != "/Applications/Bx.app" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := bundleRootFromExecutable("/usr/local/bin/bx"); err == nil {
		t.Fatal("want error for non-bundle executable")
	}
	got, err = bundleRootFromExecutable("/Users/a/Downloads/Bx.app/Contents/Resources/bx-cli")
	if err != nil || got != "/Users/a/Downloads/Bx.app" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// 升级确认默认是「不」:直接回车、空白、任何看不懂的回答都不算同意 ——
// 这一步会断网,只有明确说 y 才继续。
func TestConfirmationAcceptedOnlyOnExplicitYes(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", " yes \n"} {
		if !confirmationAccepted(answer) {
			t.Fatalf("%q 应视为同意", answer)
		}
	}
	for _, answer := range []string{"\n", "", "n\n", "no\n", "ye\n", "sure\n"} {
		if confirmationAccepted(answer) {
			t.Fatalf("%q 不该视为同意", answer)
		}
	}
}
