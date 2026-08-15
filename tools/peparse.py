# PE import-table dump: python peparse.py <path-to-dll>
import struct
import sys
path = sys.argv[1] if len(sys.argv) > 1 else "WebView2Loader.dll"
data = open(path, 'rb').read()
pe_off = struct.unpack('<I', data[0x3C:0x40])[0]
opt_off = pe_off + 24
magic = struct.unpack('<H', data[opt_off:opt_off+2])[0]
dd_off = opt_off + (112 if magic == 0x20b else 96)

def rva2off(rva):
    n = struct.unpack('<H', data[pe_off+6:pe_off+8])[0]
    osz = struct.unpack('<H', data[pe_off+20:pe_off+22])[0]
    so = pe_off + 24 + osz
    for i in range(n):
        o = so + i*40
        vs, va, rs, rp = struct.unpack('<IIII', data[o+8:o+24])
        if va <= rva < va+vs:
            return rp + (rva - va)
    return None

names = ['Export','Import','Resource','Exception','Security','BaseReloc','Debug','Arch','GlobalPtr','TLS','LoadCfg','BoundImport','IAT','DelayImport','CLR','Reserved']
dirs = []
for i in range(16):
    va, size = struct.unpack('<II', data[dd_off+i*8:dd_off+i*8+8])
    dirs.append((va, size))
for i, (va, size) in enumerate(dirs):
    if va:
        print(f"{names[i]:12s} va={va:#x} size={size:#x}")

# regular import table
imp_va, imp_size = dirs[1]
if imp_va:
    off = rva2off(imp_va)
    print("\nRegular imports:")
    while True:
        orig, ts, fwd, name_rva, first_thunk = struct.unpack('<5I', data[off:off+20])
        if name_rva == 0:
            break
        no = rva2off(name_rva)
        dll = data[no:no+80].split(b'\0')[0].decode()
        print(f"  {dll}: origFirstThunk={orig:#x}")
        off += 20

# delay import dir
diva, disize = dirs[13]
if diva:
    off = rva2off(diva)
    print("\nDelay imports:")
    while True:
        attrs, name_rva, mod_handle, iat_rva, int_rva, bound_iat, unload_iat, ts = struct.unpack('<8I', data[off:off+32])
        if attrs == 0 and name_rva == 0:
            break
        no = rva2off(name_rva)
        dll = data[no:no+80].split(b'\0')[0].decode()
        print(f"  DLL {dll}: IAT={iat_rva:#x} boundIAT={bound_iat:#x}")
        # enumerate entries from IAT
        iat_off = rva2off(iat_rva)
        io = iat_off
        i = 0
        while True:
            rva = struct.unpack('<I', data[io:io+4])[0]
            if rva == 0:
                break
            if not (rva & 0x80000000):
                no2 = rva2off(rva)
                if no2 is not None:
                    hint = struct.unpack('<H', data[no2:no2+2])[0]
                    nm = data[no2+2:no2+2+128].split(b'\0')[0].decode()
                    print(f"    [{iat_rva+i*8:#x}] {nm}")
            else:
                print(f"    [{iat_rva+i*8:#x}] ordinal {rva:#x}")
            io += 8
            i += 1
        off += 32
