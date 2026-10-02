package checkpoint

// Windows does not support syncing the read-only directory handles returned by
// os.Open. Checkpoint files are synced individually before publication; directory
// metadata has no equivalent durability guarantee here.
func syncDirectory(_ string) error {
	return nil
}
