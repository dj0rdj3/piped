package execution

import "strconv"

// Send bounded ASCII lines over the echo-free terminal after elevation. This
// avoids canonical line limits and terminal controls in arbitrary secret text.
// Decode through pipes, never command arguments or files, and preserve trailing
// newlines with a NUL delimiter before allowing interactive input to attach.
func environmentBootstrap(count int, marker string) string {
	if count == 0 {
		return ""
	}
	return `command -v base64 >/dev/null || { printf '%s\n' 'Piped: job secrets require base64 on the host.' >&2; exit 1; }
stty -echo </dev/tty || exit 1
printf '\036` + marker + `:ready\037'
for ((piped_index=0; piped_index<` + strconv.Itoa(count) + `; piped_index++)); do
    IFS= read -r piped_secret_name </dev/tty || exit 1
    IFS= read -r piped_secret_parts </dev/tty || exit 1
    [[ "$piped_secret_parts" =~ ^[1-6]$ ]] || exit 1
    piped_secret_encoded=
    for ((piped_part=0; piped_part<piped_secret_parts; piped_part++)); do
        IFS= read -r piped_secret_chunk </dev/tty || exit 1
        piped_secret_encoded+=$piped_secret_chunk
    done
    IFS= read -r -d '' piped_secret_value < <(
        printf '%s' "$piped_secret_encoded" | base64 -d && printf '\0'
    ) || exit 1
    piped_secret_names+=("$piped_secret_name")
    piped_secret_values+=("$piped_secret_value")
done
unset piped_secret_encoded piped_secret_chunk piped_secret_parts piped_secret_value piped_secret_name
`
}
