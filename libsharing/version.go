package libsharing

import "runtime"

func Version() string {
	return "0.1.0 (" + runtime.Version() + ")"
}
