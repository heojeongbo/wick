package config

import "os"

// hostname is a variable rather than a call so that a test can make it refuse.
// There is no other way to reach the branch: a machine that answers to
// [os.Hostname] answers to it every time.
var hostname = os.Hostname

type IdentityConfig struct {
	// What this machine is called in the name an object is given. Left unsaid
	// it is the hostname, which is what keeps several machines carrying the
	// same kind of file into the same bucket from writing over each other
	// without anybody having to write a different config for each of them.
	Name string `yaml:"name"`
}
