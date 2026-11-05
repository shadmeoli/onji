import re
import subprocess

limit = int(sys.argv[1])
include, exclude = set(json.loads(sys.argv[2])), set(json.loads(sys.argv[3]))
result = {}
for family in ('4', '6'):
    child = subprocess.Popen(['ss', '-' + family, '-H', '-ltnp'], stdout=subprocess.PIPE,
                             text=True, encoding='utf-8', errors='replace')
    try:
        for line in child.stdout:
            parts = line.split(None, 5)
            if len(parts) < 5:
                fail('Unexpected ss output')
            address, port_text = parts[3].rsplit(':', 1)
            port = int(port_text)
            if port in exclude or (include and port not in include):
                continue
            address = address.strip('[]')
            if address == '*':
                address = '0.0.0.0' if family == '4' else '::'
            key = (address, port)
            record = {'address': address, 'port': port, 'pid': 0, 'name': '', 'identity': ''}
            for match in re.finditer(r'pid=(\d+)', parts[5] if len(parts) > 5 else ''):
                try:
                    p = process_info(int(match.group(1)))
                    record.update({k: p[k] for k in ('pid', 'name', 'identity')})
                    break
                except (FileNotFoundError, ProcessLookupError, PermissionError):
                    continue
            if key not in result or (not result[key]['pid'] and record['pid']):
                result[key] = record
            if len(result) > limit:
                fail('Service limit exceeded; narrow include_ports or increase max_services_per_host')
        if child.wait() != 0:
            fail('ss failed; install iproute2 on the remote host')
    finally:
        child.stdout.close()
        if child.poll() is None:
            child.kill()
        child.wait()
print(json.dumps(list(result.values())))
