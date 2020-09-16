import os
import os.path
import sys

try:
    import yaml
except ImportError:
    print("Please install pyyaml")
    sys.exit(1)

import zipfile


def _read_yaml(manifest_path):
    with open(manifest_path) as f:
        try:
            return yaml.load(f)
        except Exception as e:
            print("Unable to parse yaml data in %s: %s" % (manifest_path, e))
            sys.exit(1)


def parse_package_manifest(manifest_path):
    try:
        data = _read_yaml(manifest_path)
    except Exception as e:
        print("Unable to read file %s: %s" % (manifest_path, e))
        sys.exit(1)
    try:
        channels = data.get('channels', [])
    except AttributeError:
        print("Parsed YAML is not a dict: %s" % data)
        sys.exit(1)
    for channel in channels:
        if channel.get('name') == 'alpha':
            currentCSV = channel['currentCSV']
            break
    try:
        # by convention the version starts with a 'v', we only want
        # the actual version number
        version = currentCSV.split('.', 1)[1][1:]
    except IndexError:
        print("Cannot find version in current CSV name: %s" % currentCSV)
        sys.exit(1)
    return version


def make_zip_bundle(manifest_file, version, zip_file):
    bundle_files = [f for f in os.listdir(version)
                    if os.path.isfile(os.path.join(version, f))]
    with zipfile.ZipFile(zip_file, 'w') as bundle:
        bundle.write(manifest_file)
        for bundle_file in bundle_files:
            bundle.write("%s/%s" % (version, bundle_file),
                         arcname=bundle_file)
    print("Zip bundle %s ready" % zip_file)


def main():
    if len(sys.argv) < 2:
        print("Missing package manifest path")
        sys.exit(1)
    version = parse_package_manifest(sys.argv[1])
    if len(sys.argv) > 2:
        zip_file = sys.argv[2]
    else:
        zip_file = 'nsx-ncp-operator-bundle.zip'
    make_zip_bundle(sys.argv[1], version, zip_file)

if __name__ == '__main__':
    main()
