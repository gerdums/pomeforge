#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

usage() {
  printf '%s\n' "Usage: $0 /path/to/prebuilt/pomeforge" >&2
}

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

pomeforge_source=$1
if [ ! -f "$pomeforge_source" ]; then
  printf '%s\n' "Prebuilt Pomeforge binary not found: $pomeforge_source" >&2
  exit 1
fi

case $pomeforge_source in
  /*) ;;
  *) pomeforge_source=$(CDPATH= cd -- "$(dirname -- "$pomeforge_source")" && pwd)/$(basename -- "$pomeforge_source") ;;
esac

case ${XDG_DATA_HOME:-} in
  /*) pomeforge_data_home=$XDG_DATA_HOME ;;
  *) pomeforge_data_home=$HOME/.local/share ;;
esac

installer_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
pomeforge_root=$pomeforge_data_home/pomeforge
pomeforge_bin_dir=$pomeforge_root/bin
applications_dir=$pomeforge_data_home/applications
icons_dir=$pomeforge_data_home/icons/hicolor/scalable/apps
launcher_path=$pomeforge_bin_dir/pomeforge-workspace
desktop_path=$applications_dir/pomeforge.desktop
icon_path=$icons_dir/pomeforge.svg

# Desktop Exec values are ASCII strings, executable names may not contain "=",
# and percent sequences are interpreted as field codes. Fail before any writes
# when the installed launcher path cannot be represented reliably.
case $launcher_path in
  *%* | *=*)
    printf '%s\n' "The Pomeforge install location cannot be represented in a desktop entry." >&2
    exit 1
    ;;
esac
case $launcher_path in
  *[!\ -~]*)
    printf '%s\n' "The Pomeforge install location contains unsupported characters." >&2
    exit 1
    ;;
esac

mkdir -p -- "$pomeforge_bin_dir" "$applications_dir" "$icons_dir" "$pomeforge_root/workspace"
install -m 0755 "$pomeforge_source" "$pomeforge_bin_dir/pomeforge"
install -m 0755 "$installer_dir/pomeforge-workspace" "$launcher_path"
install -m 0644 "$installer_dir/pomeforge.svg" "$icon_path"

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
desktop_tmp=$(mktemp "$applications_dir/.pomeforge.desktop.XXXXXX")
trap 'rm -f -- "$desktop_tmp"' EXIT HUP INT TERM
while IFS= read -r desktop_line || [ -n "$desktop_line" ]; do
  if [ "$desktop_line" = "Exec=@POMEFORGE_LAUNCHER@" ]; then
    printf 'Exec="%s"\n' "$escaped_launcher"
  else
    printf '%s\n' "$desktop_line"
  fi
done < "$installer_dir/pomeforge.desktop.in" > "$desktop_tmp"
chmod 0644 "$desktop_tmp"
mv -f -- "$desktop_tmp" "$desktop_path"
trap - EXIT HUP INT TERM

printf '%s\n' "Pomeforge was installed for the current user."
printf '%s\n' "Desktop entry: $desktop_path"
printf '%s\n' "Workspace: $pomeforge_root/workspace"
