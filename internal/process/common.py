import json
import os
import sys

with open('/proc/sys/kernel/random/boot_id') as f:
    boot_id = f.read().strip()

def process_info(pid):
    path = '/proc/' + str(pid)
    with open(path + '/stat') as f:
        stat = f.read(8192)
    end = stat.rfind(')')
    fields = stat[end + 2:].split()
    start = fields[19]  # field 22, after pid and parenthesized comm
    uid = os.stat(path).st_uid
    return {'pid': pid, 'name': stat[stat.find('(') + 1:end],
            'identity': boot_id + ':' + start, 'uid': uid}

def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)
