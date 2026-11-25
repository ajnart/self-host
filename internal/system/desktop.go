package system

// DesktopCommand runs a desktop operation as the invoking user when the CLI
// is elevated for Docker. The desktop command receives only session variables
// needed to reach that person's desktop.
func (d *Deps) DesktopCommand(cmd Cmd) (Cmd, error) {
	if d.GOOS != "linux" || d.Getenv("SUDO_USER") == "" {
		return cmd, nil
	}
	current, err := d.CurrentUser()
	if err != nil || current.UID != 0 {
		return cmd, err
	}
	owner, err := d.Owner()
	if err != nil || owner.UID == 0 {
		return cmd, err
	}
	args := []string{"-u", owner.Name, "--", "env", "-i",
		"HOME=" + owner.Home, "USER=" + owner.Name, "LOGNAME=" + owner.Name,
		"PATH=/usr/local/bin:/usr/bin:/bin"}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE", "XDG_DATA_HOME", "OMARCHY_PATH", "LANG", "LC_ALL"} {
		if value := d.Getenv(key); value != "" {
			args = append(args, key+"="+value)
		}
	}
	cmd.Args = append(append(args, cmd.Name), cmd.Args...)
	cmd.Name = "runuser"
	return cmd, nil
}
