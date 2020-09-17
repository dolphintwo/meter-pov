import os
import re
import sys

pattern = re.compile(r'(\d+[.]\d+[.]\d+)-beta(\d+)')
if __name__ == "__main__":
    version = ""
    with open('./cmd/meter/VERSION', 'r') as f:
        version = f.read().strip()

    m = pattern.match(version)
    if not m:
        print('version %s is not valid' % version)
        sys.exit(0)

    major = m[1]
    beta = m[2]
    nextVersion = '%s-beta%d' % (major, int(beta)+1)
    print('next version:', nextVersion)

    with open('./cmd/meter/VERSION', 'w') as v:
        v.write(nextVersion+"\n")

    meterYml = ''
    with open('./api/doc/meter.yaml', 'r') as m:
        meterYml = m.read()
    meterYml = meterYml.replace(version, nextVersion)
    with open('./api/doc/meter.yaml', 'w') as o:
        o.write(meterYml)
    os.system('cd api/doc && go-bindata -pkg=doc ./meter.yaml swagger-ui')
