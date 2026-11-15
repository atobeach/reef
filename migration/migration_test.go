package migration

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestParseOrdersMigrationsByVersionAndRefusesWhatItCannotRun(t *testing.T) {
	cases := []struct {
		name  string
		files fstest.MapFS
		want  []string
		// err is "" when the files parse, else what the refusal says.
		err string
	}{
		{
			name: "ordered by version, other files ignored",
			files: fstest.MapFS{
				"00010_add_index.sql":        {Data: []byte("-- up")},
				"00002_create_users.sql":     {Data: []byte("-- up")},
				"README.md":                  {Data: []byte("notes")},
				"archive/00001_old_base.sql": {Data: []byte("-- nested files are not migrations")},
			},
			want: []string{"00002_create_users", "00010_add_index"},
		},
		{name: "no version", files: fstest.MapFS{"create_users.sql": {}}, err: "create_users.sql: the name must be NNNNN_name.sql"},
		{name: "no name", files: fstest.MapFS{"00001.sql": {}}, err: "00001.sql: the name must be NNNNN_name.sql"},
		{name: "version zero", files: fstest.MapFS{"00000_base.sql": {}}, err: "00000_base.sql: the name must be NNNNN_name.sql"},
		{
			name:  "a shared version",
			files: fstest.MapFS{"00003_a.sql": {}, "3_b.sql": {}},
			err:   "share version 3",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			migrations, err := Parse(c.files)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one containing %q", err, c.err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			var names []string
			for _, migration := range migrations {
				names = append(names, migration.Name)
			}

			if strings.Join(names, ",") != strings.Join(c.want, ",") {
				t.Fatalf("migrations = %v, want %v", names, c.want)
			}
		})
	}
}

func TestTheFingerprintChangesExactlyWhenAMigrationDoes(t *testing.T) {
	base := fstest.MapFS{
		"00001_base.sql": {Data: []byte("CREATE TABLE users ();")},
		"00002_more.sql": {Data: []byte("ALTER TABLE users ADD x int;")},
	}

	fingerprint := func(fsys fstest.MapFS) string {
		t.Helper()

		hash, err := Fingerprint(fsys)
		if err != nil {
			t.Fatal(err)
		}

		return hash
	}

	with := func(name string, data string) fstest.MapFS {
		changed := fstest.MapFS{}
		for file, contents := range base {
			changed[file] = contents
		}
		changed[name] = &fstest.MapFile{Data: []byte(data)}

		return changed
	}

	original := fingerprint(base)
	if fingerprint(base) != original {
		t.Fatal("the same files must hash the same")
	}

	if fingerprint(with("NOTES.md", "not a migration")) != original {
		t.Error("a non-migration file changed the fingerprint")
	}

	for name, changed := range map[string]fstest.MapFS{
		"an edited migration": with("00002_more.sql", "ALTER TABLE users ADD y int;"),
		"a new migration":     with("00003_new.sql", "SELECT 1;"),
		"a renamed migration": {"00001_base.sql": base["00001_base.sql"], "00002_renamed.sql": base["00002_more.sql"]},
	} {
		if fingerprint(changed) == original {
			t.Errorf("%s kept the fingerprint", name)
		}
	}
}
