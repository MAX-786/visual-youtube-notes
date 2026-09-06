package pipeline

import "testing"

func TestVideoIDAcceptsEverySpecForm(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=aircAruvnKk":                        "aircAruvnKk",
		"https://youtube.com/watch?v=aircAruvnKk&t=42s":                      "aircAruvnKk",
		"https://m.youtube.com/watch?v=aircAruvnKk":                          "aircAruvnKk",
		"https://youtu.be/aircAruvnKk":                                       "aircAruvnKk",
		"https://youtu.be/aircAruvnKk?si=abc":                                "aircAruvnKk",
		"https://www.youtube.com/shorts/aircAruvnKk":                         "aircAruvnKk",
		"https://www.youtube.com/live/aircAruvnKk?feature=share":             "aircAruvnKk",
		"https://www.youtube.com/embed/aircAruvnKk":                          "aircAruvnKk",
		"https://www.youtube.com/watch?v=aircAruvnKk&list=PLx&index=3":       "aircAruvnKk",
		"www.youtube.com/watch?v=aircAruvnKk":                                "aircAruvnKk",
		"aircAruvnKk":                                                        "aircAruvnKk",
		"https://music.youtube.com/watch?v=aircAruvnKk":                      "aircAruvnKk",
		"  https://www.youtube.com/watch?v=aircAruvnKk  ":                    "aircAruvnKk",
		"https://www.youtube.com/watch?feature=youtu.be&v=aircAruvnKk&list=": "aircAruvnKk",
	}
	for input, want := range cases {
		got, err := VideoID(input)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestVideoIDRejectsEverythingElse(t *testing.T) {
	for _, input := range []string{
		"",
		"https://vimeo.com/12345",
		"https://www.youtube.com/playlist?list=PLx",
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/channel/UCabc",
		"https://evil.example/watch?v=aircAruvnKk",
		"https://www.youtube.com.evil.example/watch?v=aircAruvnKk",
		"not a url at all",
	} {
		if got, err := VideoID(input); err == nil {
			t.Errorf("%q: accepted as %q", input, got)
		}
	}
}
