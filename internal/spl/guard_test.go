package spl

import "testing"

func TestCheckSafe(t *testing.T) {
	cases := []struct {
		name  string
		spl   string
		allow []string
		want  string // blocked command, or "" for safe
	}{
		{"plain search", "index=main sourcetype=syslog", nil, ""},
		{"stats pipeline", "index=main | stats count by host", nil, ""},
		{"generating command", "| tstats count where index=main by host", nil, ""},
		{"delete blocked", "index=main | delete", nil, "delete"},
		{"collect blocked", "index=main | collect index=summary", nil, "collect"},
		{"outputlookup blocked", "index=main | stats count | outputlookup out.csv", nil, "outputlookup"},
		{"outputcsv blocked", "index=main | outputcsv results", nil, "outputcsv"},
		{"sendemail blocked", "index=main | sendemail to=a@example.com", nil, "sendemail"},
		{"case insensitive", "index=main | DELETE", nil, "delete"},
		{"blocked in subsearch", "index=a [ search index=b | outputlookup x ]", nil, "outputlookup"},
		{"subsearch leader is checked", "index=a [| inputlookup allowed ]", nil, ""},
		{"allow overrides block", "index=main | collect index=summary", []string{"collect"}, ""},
		{"allow is per-command", "index=main | collect | delete", []string{"collect"}, "delete"},
		{"allow normalizes case", "index=main | collect", []string{" Collect "}, ""},
		{"field named delete is safe", "index=main delete=true | stats count", nil, ""},
		{"lookup command is safe", "index=main | lookup users uid", nil, ""},
		{"inputlookup is safe", "| inputlookup mylookup", nil, ""},
		// Conservative false positive, documented behavior: quoted pipes are
		// still scanned. Blocking here is acceptable (safe side).
		{"quoted pipe false positive", `index=main msg="a | delete b"`, nil, "delete"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckSafe(tc.spl, tc.allow)
			if got != tc.want {
				t.Errorf("CheckSafe(%q, %v) = %q; want %q", tc.spl, tc.allow, got, tc.want)
			}
		})
	}
}
