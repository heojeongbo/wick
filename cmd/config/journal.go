package config

type JournalConfig struct {
	// Path is the file the records are kept in.
	//
	// It has to be somewhere that survives a restart, or the daemon carries
	// everything again every time it starts -- which costs the link and, with
	// a retention that deletes, is the difference between tidying up and
	// nothing ever being tidied.
	Path string `yaml:"path"`
}
