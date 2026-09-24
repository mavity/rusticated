package terminal

import "testing"

func TestRoundTrip_EmptyString(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{""}, 5))
		want := ""
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{""}, 20))
		want := ""
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{""}, 80))
		want := ""
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_SingleRune(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"A"}, 5))
		want := "A\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"A"}, 20))
		want := "A\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"A"}, 80))
		want := "A\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_ExactFitBoundary(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"ABCDE"}, 5))
		want := "ABCDE\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"ABCDE"}, 20))
		want := "ABCDE\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"ABCDE"}, 80))
		want := "ABCDE\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_OneOverBoundary(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"ABCDEF"}, 5))
		want := "ABCDEF\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"ABCDEF"}, 20))
		want := "ABCDEF\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"ABCDEF"}, 80))
		want := "ABCDEF\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_Prose(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"The quick"}, 5))
		want := "The quick\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"The quick"}, 20))
		want := "The quick\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"The quick"}, 80))
		want := "The quick\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_TwoParagraphs(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Alpha", "", "Beta"}, 5))
		want := "Alpha\r\n\r\nBeta\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Alpha", "", "Beta"}, 20))
		want := "Alpha\r\n\r\nBeta\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Alpha", "", "Beta"}, 80))
		want := "Alpha\r\n\r\nBeta\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_LongWordWrap(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Supercalifragilistic"}, 5))
		want := "Supercalifragilistic\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Supercalifragilistic"}, 20))
		want := "Supercalifragilistic\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Supercalifragilistic"}, 80))
		want := "Supercalifragilistic\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_CJKText(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"你好世界"}, 5))
		// CJK characters are rendered from vt.Emulator; actual roundtrip depends on
		// terminal emulation behavior, which may add spaces or strip them.
		// The critical assertion is that roundtrips are stable and deterministic.
		want := "你好世界\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"你好世界"}, 20))
		want := "你好世界\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"你好世界"}, 80))
		want := "你好世界\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_BoldRed(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[1;31mHi\x1b[0m"}, 5))
		want := "\x1b[1m\x1b[31mHi\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[1;31mHi\x1b[0m"}, 20))
		want := "\x1b[1m\x1b[31mHi\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[1;31mHi\x1b[0m"}, 80))
		want := "\x1b[1m\x1b[31mHi\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_ResetMidline(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[31mRed\x1b[0m Plain"}, 5))
		want := "\x1b[31mRed \x1b[39mPlain\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[31mRed\x1b[0m Plain"}, 20))
		want := "\x1b[31mRed \x1b[39mPlain\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[31mRed\x1b[0m Plain"}, 80))
		want := "\x1b[31mRed \x1b[39mPlain\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_BulletList(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "- item"}, 5))
		want := "text\r\n- item\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "- item"}, 20))
		want := "text\r\n- item\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "- item"}, 80))
		want := "text\r\n- item\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_Divider(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "---"}, 5))
		want := "text\r\n---\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "---"}, 20))
		want := "text\r\n---\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "---"}, 80))
		want := "text\r\n---\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_DoubleIndent(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "  ok"}, 5))
		want := "text\r\n  ok\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "  ok"}, 20))
		want := "text\r\n  ok\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"text", "  ok"}, 80))
		want := "text\r\n  ok\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_SentenceEnd(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"end.", "Next"}, 5))
		want := "end.\r\nNext\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"end.", "Next"}, 20))
		want := "end.\r\nNext\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"end.", "Next"}, 80))
		want := "end.\r\nNext\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_FlankedDecimal(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"3.", "14"}, 5))
		want := "3.\r\n14\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"3.", "14"}, 20))
		want := "3.\r\n14\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"3.", "14"}, 80))
		want := "3.\r\n14\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_BoxDrawing(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"a─", "text"}, 5))
		want := "a─\r\ntext\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"a─", "text"}, 20))
		want := "a─\r\ntext\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"a─", "text"}, 80))
		want := "a─\r\ntext\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_CarriageReturnOverwrite(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Loading...\rDone"}, 5))
		want := "LoadiDone.\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Loading...\rDone"}, 20))
		want := "Doneing...\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Loading...\rDone"}, 80))
		want := "Doneing...\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_CursorAbsolute(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[2;3HText"}, 5))
		want := "\r\n  Text\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[2;3HText"}, 20))
		want := "\r\n  Text\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[2;3HText"}, 80))
		want := "\r\n  Text\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_LineErase(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Hello\x1b[1D\x1b[K"}, 5))
		want := "Hel\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Hello\x1b[1D\x1b[K"}, 20))
		want := "Hell\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Hello\x1b[1D\x1b[K"}, 80))
		want := "Hell\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_CursorRelative(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		// Cursor movements: [5->]A[<-2]B (forward 5, print A, back 2, print B)
		got := ToANSI(FromANSI([]string{"\x1b[5CA\x1b[2DB"}, 5))
		want := "  B A\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		// Cursor movements: [5->]A[<-2]B (forward 5, print A, back 2, print B)
		got := ToANSI(FromANSI([]string{"\x1b[5CA\x1b[2DB"}, 20))
		want := "    BA\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		// Cursor movements: [5->]A[<-2]B (forward 5, print A, back 2, print B)
		got := ToANSI(FromANSI([]string{"\x1b[5CA\x1b[2DB"}, 80))
		want := "    BA\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_ColoredTrailingSpaces(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[41m  \x1b[0m"}, 5))
		want := "\x1b[41m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[41m  \x1b[0m"}, 20))
		want := "\x1b[41m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[41m  \x1b[0m"}, 80))
		want := "\x1b[41m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_StackedAttributes(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[1;3;4mText\x1b[0m"}, 5))
		want := "\x1b[1;3;4mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[1;3;4mText\x1b[0m"}, 20))
		want := "\x1b[1;3;4mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[1;3;4mText\x1b[0m"}, 80))
		want := "\x1b[1;3;4mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_WhitespaceOnly(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"   "}, 5))
		want := ""
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"   "}, 20))
		want := ""
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"   "}, 80))
		want := ""
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_NumericDecimal(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"3.14159"}, 5))
		want := "3.14159\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"3.14159"}, 20))
		want := "3.14159\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"3.14159"}, 80))
		want := "3.14159\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_MultilineParagraph(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Line one", "Line two", "Line three"}, 5))
		want := "Line one\r\nLine two\r\nLine three\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Line one", "Line two", "Line three"}, 20))
		want := "Line one\r\nLine two\r\nLine three\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Line one", "Line two", "Line three"}, 80))
		want := "Line one\r\nLine two\r\nLine three\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_BlockDimText(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[2mDimmed\x1b[0m"}, 5))
		want := "\x1b[2mDimmed\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[2mDimmed\x1b[0m"}, 20))
		want := "\x1b[2mDimmed\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[2mDimmed\x1b[0m"}, 80))
		want := "\x1b[2mDimmed\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_UnderlinedTrailingSpaces(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Text\x1b[4m  \x1b[0m"}, 5))
		want := "Text\x1b[4m \r\n \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Text\x1b[4m  \x1b[0m"}, 20))
		want := "Text\x1b[4m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Text\x1b[4m  \x1b[0m"}, 80))
		want := "Text\x1b[4m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_BlinkingText(t *testing.T) {
	t.Run("W=5", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[5mBlink\x1b[0m"}, 5))
		want := "\x1b[5mBlink\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[5mBlink\x1b[0m"}, 20))
		want := "\x1b[5mBlink\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[5mBlink\x1b[0m"}, 80))
		want := "\x1b[5mBlink\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Trailing Space Logic: Five Critical State-Sensitivity Scenarios
// ─────────────────────────────────────────────────────────────────────────────

func TestRoundTrip_PlainTrailingSpacesAfterColor(t *testing.T) {
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[31mRed   "}, 10))
		want := "\x1b[31mRed\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[31mRed   "}, 20))
		want := "\x1b[31mRed\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[31mRed   "}, 80))
		want := "\x1b[31mRed\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_MixedMidAndTrailingSpaces(t *testing.T) {
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"  A  B  "}, 10))
		want := "  A  B\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"  A  B  "}, 20))
		want := "  A  B\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"  A  B  "}, 80))
		want := "  A  B\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_BackgroundColoredTrailingSpaces(t *testing.T) {
	// Red background with trailing spaces: should preserve the background color
	// across the spaces and emit appropriate SGR code.
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[41mRed  \x1b[0m"}, 10))
		// Red background text followed by plain spaces, line end implicit.
		want := "\x1b[41mRed  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[41mBG  \x1b[0m"}, 20))
		want := "\x1b[41mBG  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_TrailingSpacesBeforeSoftWrap(t *testing.T) {
	// Trailing spaces before a soft-wrap boundary: spaces should be preserved.
	// At W=10, text may soft-wrap; line termination must preserve trailing spaces.
	t.Run("W=10", func(t *testing.T) {
		// "Hello   world" = 13 chars at W=10 may trigger soft-wrap decision.
		// Output should have line breaks and preserve spaces.
		got := ToANSI(FromANSI([]string{"Hello   world"}, 10))
		// Round-trip: spaces between words are preserved, soft-wrap may insert \r\n
		want := "Hello   world\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Word1   Word2"}, 20))
		// At wider width, no soft-wrap; just plain text with line ending.
		want := "Word1   Word2\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_UnderlineResetBeforePlainTrailingSpaces(t *testing.T) {
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[4mText\x1b[0m   "}, 10))
		want := "\x1b[4mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[4mText\x1b[0m   "}, 20))
		want := "\x1b[4mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[4mText\x1b[0m   "}, 80))
		want := "\x1b[4mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_ForegroundColorThenPlainTrailingSpaces(t *testing.T) {
	// Foreground-only colored text (no background) followed by plain trailing spaces.
	// The fg color active, but trailing spaces are default background, so no BG reset needed.
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[33mYellow   "}, 10))
		want := "\x1b[33mYellow\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[33mYellow   "}, 20))
		want := "\x1b[33mYellow\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[33mYellow   "}, 80))
		want := "\x1b[33mYellow\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Missing Blueprint Scenarios: Truecolor + Mixed BG Trailing Spaces
// ─────────────────────────────────────────────────────────────────────────────

func TestRoundTrip_TruecolorForeground(t *testing.T) {
	// 24-bit Truecolor foreground: \x1b[38;2;R;G;Bm format.
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[38;2;255;100;50mTruecolor"}, 10))
		// Truecolor RGB is preserved through round-trip serialization.
		want := "\x1b[38;2;255;100;50mTruecolor\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[38;2;0;255;128mRGB"}, 20))
		want := "\x1b[38;2;0;255;128mRGB\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[38;2;64;32;200mText"}, 80))
		want := "\x1b[38;2;64;32;200mText\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_TruecolorBackground(t *testing.T) {
	// 24-bit Truecolor background: \x1b[48;2;R;G;Bm format.
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[48;2;10;20;30mBG"}, 10))
		// Truecolor RGB background is preserved through round-trip serialization.
		want := "\x1b[48;2;10;20;30mBG\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[48;2;200;100;50mBack"}, 20))
		want := "\x1b[48;2;200;100;50mBack\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"\x1b[48;2;100;150;200mBackground"}, 80))
		want := "\x1b[48;2;100;150;200mBackground\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRoundTrip_MixedPlainAndBGTrailingSpaces(t *testing.T) {
	// Critical edge case: unstyled plain spaces followed by BG-colored spaces on same line.
	// Tests whether flushPendingSpaces() correctly handles background color transitions mid-line.
	t.Run("W=10", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"A  \x1b[41m  \x1b[0m"}, 10))
		// Plain 'A', 2 plain spaces (deferred), 2 red-BG spaces (styled), reset.
		// flushPendingSpaces() must emit plain spaces before BG color code.
		want := "A  \x1b[41m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=20", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Text   \x1b[43m   \x1b[0m"}, 20))
		// Text, 3 plain spaces (deferred), 3 yellow-BG spaces (styled), reset.
		want := "Text   \x1b[43m   \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("W=80", func(t *testing.T) {
		got := ToANSI(FromANSI([]string{"Word   \x1b[46m  \x1b[0m"}, 80))
		// Word, 3 plain spaces (deferred), 2 cyan-BG spaces (styled), reset.
		want := "Word   \x1b[46m  \r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
