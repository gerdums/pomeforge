#!/bin/sh
set -eu

usage() {
  printf '%s\n' "Usage: $0 /path/to/prebuilt/orchard" >&2
}

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

orchard_source=$1
if [ ! -f "$orchard_source" ]; then
  printf '%s\n' "Prebuilt Orchard binary not found: $orchard_source" >&2
  exit 1
fi

case $orchard_source in
  /*) ;;
  *) orchard_source=$(CDPATH= cd -- "$(dirname -- "$orchard_source")" && pwd)/$(basename -- "$orchard_source") ;;
esac

case ${XDG_DATA_HOME:-} in
  /*) orchard_data_home=$XDG_DATA_HOME ;;
  *) orchard_data_home=$HOME/.local/share ;;
esac

installer_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
orchard_root=$orchard_data_home/orchard
orchard_bin_dir=$orchard_root/bin
applications_dir=$orchard_data_home/applications
icons_dir=$orchard_data_home/icons/hicolor/scalable/apps
launcher_path=$orchard_bin_dir/orchard-workspace
desktop_path=$applications_dir/orchard.desktop
icon_path=$icons_dir/orchard.svg

# Desktop Exec values are ASCII strings, executable names may not contain "=",
# and percent sequences are interpreted as field codes. Fail before any writes
# when the installed launcher path cannot be represented reliably.
case $launcher_path in
  *%* | *=*)
    printf '%s\n' "The Orchard install location cannot be represented in a desktop entry." >&2
    exit 1
    ;;
esac
if printf '%s' "$launcher_path" | LC_ALL=C grep -q '[^ -~]'; then
  printf '%s\n' "The Orchard install location contains unsupported characters." >&2
  exit 1
fi

mkdir -p -- "$orchard_bin_dir" "$applications_dir" "$icons_dir" "$orchard_root/workspace"
install -m 0755 "$orchard_source" "$orchard_bin_dir/orchard"
install -m 0755 "$installer_dir/orchard-workspace" "$launcher_path"
install -m 0644 "$installer_dir/orchard.svg" "$icon_path"

# Desktop Exec values have a general string escape layer followed by command
# quoting. A literal backslash therefore needs four raw backslashes. Dollar and
# backtick quoting escapes need two; a quote itself needs one.
escaped_launcher=$(printf '%s' "$launcher_path" | LC_ALL=C awk '
  BEGIN { ORS = "" }
  {
    for (position = 1; position <= length($0); position += 1) {
      character = substr($0, position, 1)
      if (character == "\\") {
        printf "%c%c%c%c", 92, 92, 92, 92
      } else if (character == "\"") {
        printf "%c%c", 92, 34
      } else if (character == "$" || character == "`") {
        printf "%c%c%s", 92, 92, character
      } else {
        printf "%s", character
      }
    }
  }
')
desktop_tmp=$(mktemp "$applications_dir/.orchard.desktop.XXXXXX")
trap 'rm -f -- "$desktop_tmp"' EXIT HUP INT TERM
while IFS= read -r desktop_line || [ -n "$desktop_line" ]; do
  if [ "$desktop_line" = "Exec=@ORCHARD_LAUNCHER@" ]; then
    printf 'Exec="%s"\n' "$escaped_launcher"
  else
    printf '%s\n' "$desktop_line"
  fi
done < "$installer_dir/orchard.desktop.in" > "$desktop_tmp"
chmod 0644 "$desktop_tmp"
mv -f -- "$desktop_tmp" "$desktop_path"
trap - EXIT HUP INT TERM

printf '%s\n' "Orchard was installed for the current user."
printf '%s\n' "Desktop entry: $desktop_path"
printf '%s\n' "Workspace: $orchard_root/workspace"
