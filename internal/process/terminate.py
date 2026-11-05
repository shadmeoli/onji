import signal

pid, expected, action = int(sys.argv[1]), sys.argv[2], sys.argv[3]
if pid <= 1 or pid == os.getpid() or action not in ('TERM', 'KILL'):
    fail('Invalid process or signal')
if not hasattr(os, 'pidfd_open') or not hasattr(signal, 'pidfd_send_signal'):
    fail('Safe termination requires Python 3.9+ and Linux pidfd support')
try:
    fd = os.pidfd_open(pid)
    try:
        current = process_info(pid)
        if current['identity'] != expected:
            fail('Process identity changed; refresh before trying again')
        if current['uid'] != os.geteuid():
            fail('Permission denied: only processes owned by the SSH user may be stopped')
        signal.pidfd_send_signal(fd, getattr(signal, 'SIG' + action))
    finally:
        os.close(fd)
except OSError as e:
    fail(str(e))
print('{}')
