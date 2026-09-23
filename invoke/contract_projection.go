package invoke

// sourceLocation projects an OBI source's optional location onto
// InvocationSource, where an absent location is the empty string. A
// conformant document never has a present empty location (OBI-D-02), so the
// projection loses nothing for a document that passed preparation.
func sourceLocation(location *string) string {
	if location == nil {
		return ""
	}
	return *location
}

// cloneSelector copies a selector's presence and value, so a record handed to
// a caller never aliases the prepared snapshot.
func cloneSelector(selector *string) *string {
	if selector == nil {
		return nil
	}
	value := *selector
	return &value
}
