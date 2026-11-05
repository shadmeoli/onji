limit = int(sys.argv[1])
result = []
with os.scandir('/proc') as entries:
    for entry in entries:
        if not entry.name.isdigit() or int(entry.name) == os.getpid():
            continue
        try:
            p = process_info(int(entry.name))
            if p['uid'] != os.geteuid():
                continue
            result.append(p)
            if len(result) > limit:
                fail('Process limit exceeded; increase max_processes_per_host')
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
print(json.dumps(sorted(result, key=lambda p: p['pid'])))
