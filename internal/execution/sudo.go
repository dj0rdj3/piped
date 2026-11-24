package execution

// Sudo inside a managed script is a compatibility command: elevation has
// already been decided for the entire script from the job and host snapshot.
// Parse argv, never script text, so quoting, pipelines, and here-docs survive.
// This is convenience for trusted administrator scripts, not a shell sandbox.
const managedSudo = `sudo() {
    local piped_flags piped_user
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --) shift; break ;;
            --non-interactive|--stdin|--preserve-env|--preserve-env=*|--set-home|--reset-timestamp)
                shift ;;
            -p|--prompt)
                if [ "$#" -lt 2 ]; then
                    printf '%s\n' 'Piped: sudo prompt option requires a value.' >&2
                    return 2
                fi
                shift 2 ;;
            --prompt=*) shift ;;
            -u|--user)
                if [ "$#" -lt 2 ]; then
                    printf '%s\n' 'Piped: sudo user option requires a value.' >&2
                    return 2
                fi
                piped_user=$2
                shift 2
                case "$piped_user" in
                    root|'#0') ;;
                    *)
                        printf '%s\n' 'Piped: sudo user switching is not supported; use job and host settings.' >&2
                        return 2
                        ;;
                esac ;;
            --user=*|-u?*)
                piped_user=${1#--user=}
                piped_user=${piped_user#-u}
                shift
                case "$piped_user" in
                    root|'#0') ;;
                    *)
                        printf '%s\n' 'Piped: sudo user switching is not supported; use job and host settings.' >&2
                        return 2
                        ;;
                esac ;;
            -*)
                piped_flags=${1#-}
                while [ -n "$piped_flags" ]; do
                    case "${piped_flags:0:1}" in
                        n|S|E|H|k) ;;
                        *)
                            printf '%s\n' 'Piped: unsupported sudo option; use job and host settings.' >&2
                            return 2
                            ;;
                    esac
                    piped_flags=${piped_flags:1}
                done
                shift ;;
            *) break ;;
        esac
    done
    if [ "$#" -eq 0 ]; then
        printf '%s\n' 'Piped: sudo requires a command; use job and host settings.' >&2
        return 2
    fi
    if [[ "$1" =~ ^[a-zA-Z_][a-zA-Z0-9_]*= ]]; then
        command env -- "$@"
    else
        "$@"
    fi
}`
