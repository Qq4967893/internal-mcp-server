package adminapi

func invalidArgument(message string) error {
	return &Error{
		Kind:    ErrorKindInvalid,
		Message: message,
	}
}
