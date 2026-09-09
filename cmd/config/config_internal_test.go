package config

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// A machine whose hostname cannot be read can still carry files perfectly well;
// the name is only how they are told apart, and refusing to start over it would
// be refusing over the one thing that does not matter.
func TestAMachineThatWillNotSayWhatItIsCalled(t *testing.T) {
	x := require.New(t)

	was := hostname
	hostname = func() (string, error) { return "", errors.New("no idea") }
	t.Cleanup(func() { hostname = was })

	c := &Config{}
	x.NoError(c.Evaluate())
	x.Equal("unknown", c.Identity.Name)
}

// walk is recursive, and it is reached from itself with values that [root] would
// have refused. Every caller from outside goes through root; this is the one
// that does not.
func TestWalkingSomethingThatCannotBeWrittenInto(t *testing.T) {
	x := require.New(t)

	type inner struct {
		Name string `yaml:"name"`
	}

	visited := 0
	visit := func(string, reflect.Value) (bool, error) {
		visited++

		return false, nil
	}

	set, err := walk(reflect.ValueOf((*inner)(nil)), nil, visit)
	x.NoError(err)
	x.False(set)
	x.Zero(visited)
}

// A pointer to something that cannot be read is refused, and the field is left
// as it was rather than made and left half-read.
func TestSettingAPointerToSomethingThatDoesNotFit(t *testing.T) {
	x := require.New(t)

	var v struct {
		Level *int `yaml:"level"`
	}

	unknown, err := OverrideFromEnv(&v, []string{"WICK_LEVEL=lots"})
	x.ErrorContains(err, "WICK_LEVEL")
	x.Empty(unknown)
	x.Nil(v.Level)
}
