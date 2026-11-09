package launch

// Test hooks for the external test package.
func SniffImage(data []byte) (string, error) { return sniffImage(data) }
