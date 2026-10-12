package sync

// Action is what a sync should do with one file after comparing local, Drive
// and base.
type Action int

const (
	ActionNothing    Action = iota // neither side changed, or both changed to the same content
	ActionUpload                   // only local changed
	ActionDownload                 // only Drive changed
	ActionUpdateBase               // both changed to the same MD5: record it as the new base
	ActionConflict                 // both changed to different content
)

// Decide is the three-way compare from requirements section 9. Each argument
// is an MD5; base is the MD5 at the last successful sync. A side is "changed"
// when its MD5 differs from base.
func Decide(local, drive, base string) Action {
	localChanged, driveChanged := local != base, drive != base
	switch {
	case !localChanged && !driveChanged:
		return ActionNothing
	case localChanged && !driveChanged:
		return ActionUpload
	case !localChanged:
		return ActionDownload
	case local == drive:
		return ActionUpdateBase
	default:
		return ActionConflict
	}
}
