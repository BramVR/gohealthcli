//go:build !windows

package backup

func hardenPrivatePath(_ string, _ bool) error { return nil }

func validatePlatformPrivatePath(_ string, _ bool) error { return nil }
