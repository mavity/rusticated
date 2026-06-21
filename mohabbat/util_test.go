package mohabbat

import (
	"reflect"
	"testing"
)

func TestUpsertEnv(t *testing.T) {
	env := []string{"FOO=bar", "BAZ=qux"}

	t.Run("insert new", func(t *testing.T) {
		res := upsertEnv(env, "NEW", "value")
		expected := []string{"FOO=bar", "BAZ=qux", "NEW=value"}
		if !reflect.DeepEqual(res, expected) {
			t.Errorf("expected %v, got %v", expected, res)
		}
	})

	t.Run("update existing", func(t *testing.T) {
		res := upsertEnv(env, "FOO", "new_bar")
		expected := []string{"BAZ=qux", "FOO=new_bar"}
		if !reflect.DeepEqual(res, expected) {
			t.Errorf("expected %v, got %v", expected, res)
		}
	})

	t.Run("update case insensitive existing", func(t *testing.T) {
		res := upsertEnv(env, "foo", "new_bar2")
		expected := []string{"BAZ=qux", "foo=new_bar2"}
		if !reflect.DeepEqual(res, expected) {
			t.Errorf("expected %v, got %v", expected, res)
		}
	})
}

func TestUniqueStrings(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	expected := []string{"a", "b", "c"}
	res := uniqueStrings(input)
	if !reflect.DeepEqual(res, expected) {
		t.Errorf("expected %v, got %v", expected, res)
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in  int64
		out string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
	}
	for _, c := range cases {
		t.Run(c.out, func(t *testing.T) {
			res := formatSize(c.in)
			if res != c.out {
				t.Errorf("expected %s, got %s", c.out, res)
			}
		})
	}
}

func TestIsProject(t *testing.T) {
	// Our test execution environment is usually within the package directory
	// Make sure testdata/dummy-go and testdata/dummy-rust return true
	t.Run("go project", func(t *testing.T) {
		if !IsProject(".", "testdata/dummy-go") {
			t.Errorf("expected dummy-go to be recognized as project")
		}
	})
	t.Run("rust project", func(t *testing.T) {
		if !IsProject(".", "testdata/dummy-rust") {
			t.Errorf("expected dummy-rust to be recognized as project")
		}
	})
	t.Run("invalid project", func(t *testing.T) {
		if IsProject(".", "testdata/does-not-exist") {
			t.Errorf("expected non-existent dir to fail")
		}
	})
}
