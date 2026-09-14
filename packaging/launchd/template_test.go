package launchd

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"text/template"
)

func TestTemplateParsesAndRenders(t *testing.T) {
	tmpl, err := template.New("plist").Parse(PlistTemplate)
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, map[string]any{
		"Label":                "io.github.nozomemein.schlaflos",
		"ProgramArguments":     []string{"/Library/Application Support/schlaflos/bin/schlaflos", "reconcile"},
		"StartIntervalSeconds": 30,
		"LogPath":              "/private/var/log/schlaflos.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// The result must be well-formed XML.
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("rendered plist is not well-formed XML: %v\n%s", err, out)
		}
	}

	for _, want := range []string{
		"<key>Label</key>\n\t<string>io.github.nozomemein.schlaflos</string>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>/Library/Application Support/schlaflos/bin/schlaflos</string>\n\t\t<string>reconcile</string>\n\t</array>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>StartInterval</key>\n\t<integer>30</integer>",
		"<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>PATH</key>\n\t\t<string>/usr/bin:/bin:/usr/sbin:/sbin</string>\n\t</dict>",
		"<key>StandardOutPath</key>\n\t<string>/private/var/log/schlaflos.log</string>",
		"<key>StandardErrorPath</key>\n\t<string>/private/var/log/schlaflos.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered plist missing:\n%s\n--- got:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"KeepAlive", "UserName", "/usr/local/bin", "HOME=", "<key>Sockets</key>"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("rendered plist must not contain %q", forbidden)
		}
	}
	if strings.Count(out, "<key>EnvironmentVariables</key>") != 1 || strings.Count(out, "\t\t<key>") != 1 {
		t.Errorf("environment must contain exactly one variable (PATH):\n%s", out)
	}
}
