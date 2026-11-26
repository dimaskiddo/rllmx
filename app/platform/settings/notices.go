package settings

// Notices are the start-up notices a loader collected. A value outside its
// range that has a safe default is replaced by that default and reported here,
// once, by the configuration gate. An entry names the variable and the default
// it used, never the value that was configured.
type Notices struct {
	Warnings []string
}

func (n *Notices) warn(env, reason, def string) {
	n.Warnings = append(n.Warnings, "settings: "+env+": "+reason+"; using "+def)
}
