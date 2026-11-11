// Adds the icon and version details to a built SECquence.exe.
// Usage: node brand.mjs <built.exe> <output.exe> <version>
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import * as ResEdit from 'resedit';
import * as PELibrary from 'pe-library';

const [input, output, version] = process.argv.slice(2);
if (!input || !output || !/^\d+\.\d+\.\d+$/.test(version || '')) {
  console.error('usage: node brand.mjs <built.exe> <output.exe> <version like 2.5.0>');
  process.exit(1);
}
const here = path.dirname(fileURLToPath(import.meta.url));
const [major, minor, patch] = version.split('.').map(Number);
const exe = PELibrary.NtExecutable.from(fs.readFileSync(input));
const res = PELibrary.NtExecutableResource.from(exe);
const ico = ResEdit.Data.IconFile.from(fs.readFileSync(path.join(here, 'secquence.ico')));
ResEdit.Resource.IconGroupEntry.replaceIconsForResource(res.entries, 1, 1033, ico.icons.map((i) => i.data));
const vi = ResEdit.Resource.VersionInfo.createEmpty();
vi.setFileVersion(major, minor, patch, 0, 1033);
vi.setProductVersion(major, minor, patch, 0, 1033);
vi.setStringValues({ lang: 1033, codepage: 1200 }, {
  FileDescription: 'SECquence', ProductName: 'SECquence', CompanyName: 'SECquence', OriginalFilename: 'SECquence.exe',
  InternalName: 'SECquence', FileVersion: version, ProductVersion: version,
});
vi.outputToResourceEntries(res.entries);
res.outputResource(exe);
fs.writeFileSync(output, Buffer.from(exe.generate()));
