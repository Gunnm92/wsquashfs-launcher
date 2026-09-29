/*
 * dinput8.dll relais (32 bits) — faux ping ICMP pour Yu-Gi-Oh! 5D's Duel
 * Terminal 6 sous Wine sans CAP_NET_RAW.
 *
 * Le jeu ouvre un socket brut ICMP (ping e-amusement) ; sans CAP_NET_RAW,
 * Wine le refuse et le jeu s'arrête sur une assertion. ws2_32 est une
 * KnownDLL (jamais chargée depuis le dossier du jeu) : ce relais se glisse à
 * la place de dinput8, que game.exe importe, transmet ses 5 fonctions au vrai
 * dinput8 et, à son chargement, redirige dans la table d'imports de chaque
 * module déjà chargé les appels socket/WSASocketA/W, bind, send*, setsockopt
 * et closesocket. Seulement si la création d'un socket brut ICMP échoue, un
 * socket UDP local est rendu à la place : bind, envois et options y
 * réussissent sans effet, les réceptions ne trouvent rien (ping sans
 * réponse). Là où le vrai socket se crée (Batocera en root), rien ne change.
 */
#include <winsock2.h>
#include <windows.h>
#include <psapi.h>

/* ---- relais dinput8 ---- */

static FARPROC d_create, d_canunload, d_getclass, d_register, d_unregister;

HRESULT WINAPI my_DirectInput8Create(HINSTANCE a, DWORD b, REFIID c, LPVOID *d, LPUNKNOWN e)
{
    return ((HRESULT (WINAPI *)(HINSTANCE, DWORD, REFIID, LPVOID *, LPUNKNOWN))d_create)(a, b, c, d, e);
}
HRESULT WINAPI my_DllCanUnloadNow(void) { return ((HRESULT (WINAPI *)(void))d_canunload)(); }
HRESULT WINAPI my_DllGetClassObject(REFCLSID a, REFIID b, LPVOID *c)
{
    return ((HRESULT (WINAPI *)(REFCLSID, REFIID, LPVOID *))d_getclass)(a, b, c);
}
HRESULT WINAPI my_DllRegisterServer(void) { return ((HRESULT (WINAPI *)(void))d_register)(); }
HRESULT WINAPI my_DllUnregisterServer(void) { return ((HRESULT (WINAPI *)(void))d_unregister)(); }

/* ---- faux ping ---- */

typedef SOCKET (WINAPI *socket_fn)(int, int, int);
typedef SOCKET (WINAPI *wsasocketa_fn)(int, int, int, LPWSAPROTOCOL_INFOA, GROUP, DWORD);
typedef SOCKET (WINAPI *wsasocketw_fn)(int, int, int, LPWSAPROTOCOL_INFOW, GROUP, DWORD);
typedef int (WINAPI *bind_fn)(SOCKET, const struct sockaddr *, int);
typedef int (WINAPI *sendto_fn)(SOCKET, const char *, int, int, const struct sockaddr *, int);
typedef int (WINAPI *send_fn)(SOCKET, const char *, int, int);
typedef int (WINAPI *wsasend_fn)(SOCKET, LPWSABUF, DWORD, LPDWORD, DWORD, LPWSAOVERLAPPED, LPWSAOVERLAPPED_COMPLETION_ROUTINE);
typedef int (WINAPI *wsasendto_fn)(SOCKET, LPWSABUF, DWORD, LPDWORD, DWORD, const struct sockaddr *, int, LPWSAOVERLAPPED, LPWSAOVERLAPPED_COMPLETION_ROUTINE);
typedef int (WINAPI *setsockopt_fn)(SOCKET, int, int, const char *, int);
typedef int (WINAPI *closesocket_fn)(SOCKET);

static socket_fn r_socket;
static wsasocketa_fn r_WSASocketA;
static wsasocketw_fn r_WSASocketW;
static bind_fn r_bind;
static sendto_fn r_sendto;
static send_fn r_send;
static wsasend_fn r_WSASend;
static wsasendto_fn r_WSASendTo;
static setsockopt_fn r_setsockopt;
static closesocket_fn r_closesocket;

#define MAX_FAKE 64
static SOCKET fake[MAX_FAKE];
static CRITICAL_SECTION lock;

static int is_fake(SOCKET s)
{
    int i, r = 0;
    EnterCriticalSection(&lock);
    for (i = 0; i < MAX_FAKE; i++) if (fake[i] == s) { r = 1; break; }
    LeaveCriticalSection(&lock);
    return r;
}

static void set_fake(SOCKET s, int add)
{
    int i;
    EnterCriticalSection(&lock);
    for (i = 0; i < MAX_FAKE; i++)
    {
        if (add && fake[i] == INVALID_SOCKET) { fake[i] = s; break; }
        if (!add && fake[i] == s) { fake[i] = INVALID_SOCKET; break; }
    }
    LeaveCriticalSection(&lock);
}

/* Socket UDP local à la place du socket brut ICMP refusé. */
static SOCKET make_fake(int af)
{
    struct sockaddr_in a;
    SOCKET s;

    if (af != AF_INET) return INVALID_SOCKET;
    s = r_socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    if (s == INVALID_SOCKET) return s;
    /* recvfrom exige un socket UDP lié : 127.0.0.1, port libre. */
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_addr.s_addr = 0x0100007F; /* 127.0.0.1, ordre réseau */
    r_bind(s, (struct sockaddr *)&a, sizeof(a));
    set_fake(s, 1);
    OutputDebugStringA("fakeping : socket ICMP refusé, faux ping UDP\n");
    return s;
}

static int wants_icmp(int type, int protocol)
{
    return type == SOCK_RAW && protocol == IPPROTO_ICMP;
}

static SOCKET WINAPI h_socket(int af, int type, int protocol)
{
    SOCKET s = r_socket(af, type, protocol);
    if (s == INVALID_SOCKET && wants_icmp(type, protocol))
    {
        s = make_fake(af);
        if (s != INVALID_SOCKET) SetLastError(0);
    }
    return s;
}

static SOCKET WINAPI h_WSASocketA(int af, int type, int protocol, LPWSAPROTOCOL_INFOA info, GROUP g, DWORD flags)
{
    SOCKET s = r_WSASocketA(af, type, protocol, info, g, flags);
    if (s == INVALID_SOCKET && wants_icmp(type, protocol))
    {
        s = make_fake(af);
        if (s != INVALID_SOCKET) SetLastError(0);
    }
    return s;
}

static SOCKET WINAPI h_WSASocketW(int af, int type, int protocol, LPWSAPROTOCOL_INFOW info, GROUP g, DWORD flags)
{
    SOCKET s = r_WSASocketW(af, type, protocol, info, g, flags);
    if (s == INVALID_SOCKET && wants_icmp(type, protocol))
    {
        s = make_fake(af);
        if (s != INVALID_SOCKET) SetLastError(0);
    }
    return s;
}

/* Le jeu lie son socket ICMP au "port" 1 : refusé pour de l'UDP sans
 * privilège, et sans objet ici. */
static int WINAPI h_bind(SOCKET s, const struct sockaddr *addr, int len)
{
    if (is_fake(s)) return 0;
    return r_bind(s, addr, len);
}

static int WINAPI h_sendto(SOCKET s, const char *buf, int len, int flags, const struct sockaddr *to, int tolen)
{
    if (is_fake(s)) return len;
    return r_sendto(s, buf, len, flags, to, tolen);
}

static int WINAPI h_send(SOCKET s, const char *buf, int len, int flags)
{
    if (is_fake(s)) return len;
    return r_send(s, buf, len, flags);
}

static DWORD total(LPWSABUF bufs, DWORD n)
{
    DWORD i, t = 0;
    for (i = 0; i < n; i++) t += bufs[i].len;
    return t;
}

static int WINAPI h_WSASend(SOCKET s, LPWSABUF bufs, DWORD n, LPDWORD sent, DWORD flags,
                            LPWSAOVERLAPPED ov, LPWSAOVERLAPPED_COMPLETION_ROUTINE cb)
{
    if (is_fake(s)) { if (sent) *sent = total(bufs, n); return 0; }
    return r_WSASend(s, bufs, n, sent, flags, ov, cb);
}

static int WINAPI h_WSASendTo(SOCKET s, LPWSABUF bufs, DWORD n, LPDWORD sent, DWORD flags,
                              const struct sockaddr *to, int tolen,
                              LPWSAOVERLAPPED ov, LPWSAOVERLAPPED_COMPLETION_ROUTINE cb)
{
    if (is_fake(s)) { if (sent) *sent = total(bufs, n); return 0; }
    return r_WSASendTo(s, bufs, n, sent, flags, to, tolen, ov, cb);
}

/* Options propres à l'IP brut (IP_HDRINCL...) : sans objet sur le faux. */
static int WINAPI h_setsockopt(SOCKET s, int level, int name, const char *val, int len)
{
    int r = r_setsockopt(s, level, name, val, len);
    if (r != 0 && is_fake(s)) { SetLastError(0); return 0; }
    return r;
}

static int WINAPI h_closesocket(SOCKET s)
{
    if (is_fake(s)) set_fake(s, 0);
    return r_closesocket(s);
}

/* ---- redirection des tables d'imports ---- */

struct hook { const char *name; void **real; void *repl; };

static struct hook hooks[] = {
    { "socket",      (void **)&r_socket,      (void *)h_socket },
    { "WSASocketA",  (void **)&r_WSASocketA,  (void *)h_WSASocketA },
    { "WSASocketW",  (void **)&r_WSASocketW,  (void *)h_WSASocketW },
    { "bind",        (void **)&r_bind,        (void *)h_bind },
    { "sendto",      (void **)&r_sendto,      (void *)h_sendto },
    { "send",        (void **)&r_send,        (void *)h_send },
    { "WSASend",     (void **)&r_WSASend,     (void *)h_WSASend },
    { "WSASendTo",   (void **)&r_WSASendTo,   (void *)h_WSASendTo },
    { "setsockopt",  (void **)&r_setsockopt,  (void *)h_setsockopt },
    { "closesocket", (void **)&r_closesocket, (void *)h_closesocket },
};
#define NHOOKS (sizeof(hooks) / sizeof(hooks[0]))

/* Remplace, dans les imports de ws2_32 du module, les adresses réelles par
 * celles du faux ping (imports par nom comme par ordinal : on compare les
 * adresses déjà résolues). */
static int patch_module(HMODULE mod)
{
    BYTE *base = (BYTE *)mod;
    IMAGE_DOS_HEADER *dos = (IMAGE_DOS_HEADER *)base;
    IMAGE_NT_HEADERS *nt;
    IMAGE_DATA_DIRECTORY *dir;
    IMAGE_IMPORT_DESCRIPTOR *imp;
    int patched = 0;

    if (dos->e_magic != IMAGE_DOS_SIGNATURE) return 0;
    nt = (IMAGE_NT_HEADERS *)(base + dos->e_lfanew);
    if (nt->Signature != IMAGE_NT_SIGNATURE) return 0;
    dir = &nt->OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_IMPORT];
    if (!dir->VirtualAddress || !dir->Size) return 0;

    for (imp = (IMAGE_IMPORT_DESCRIPTOR *)(base + dir->VirtualAddress); imp->Name; imp++)
    {
        IMAGE_THUNK_DATA *t;
        if (lstrcmpiA((const char *)(base + imp->Name), "ws2_32.dll")) continue;
        for (t = (IMAGE_THUNK_DATA *)(base + imp->FirstThunk); t->u1.Function; t++)
        {
            unsigned i;
            for (i = 0; i < NHOOKS; i++)
            {
                DWORD old;
                if ((void *)t->u1.Function != *hooks[i].real) continue;
                if (!VirtualProtect(&t->u1.Function, sizeof(t->u1.Function), PAGE_READWRITE, &old)) break;
                t->u1.Function = (ULONG_PTR)hooks[i].repl;
                VirtualProtect(&t->u1.Function, sizeof(t->u1.Function), old, &old);
                patched++;
                break;
            }
        }
    }
    return patched;
}

static void install_hooks(HMODULE self)
{
    HMODULE ws2 = LoadLibraryA("ws2_32.dll"), mods[512];
    DWORD needed = 0, n, i;
    unsigned h;
    int total_patched = 0;
    char msg[96];

    if (!ws2) return;
    for (h = 0; h < NHOOKS; h++)
        if (!(*hooks[h].real = (void *)GetProcAddress(ws2, hooks[h].name))) return;
    if (!K32EnumProcessModules(GetCurrentProcess(), mods, sizeof(mods), &needed)) return;
    n = needed / sizeof(HMODULE);
    if (n > 512) n = 512;
    for (i = 0; i < n; i++)
        if (mods[i] != self && mods[i] != ws2) total_patched += patch_module(mods[i]);
    wsprintfA(msg, "fakeping : %d imports ws2_32 redirigés\n", total_patched);
    OutputDebugStringA(msg);
}

BOOL WINAPI DllMain(HINSTANCE inst, DWORD reason, LPVOID reserved)
{
    (void)reserved;
    if (reason == DLL_PROCESS_ATTACH)
    {
        WCHAR path[MAX_PATH];
        HMODULE real;
        int i;

        DisableThreadLibraryCalls(inst);
        InitializeCriticalSection(&lock);
        for (i = 0; i < MAX_FAKE; i++) fake[i] = INVALID_SOCKET;
        /* Le vrai dinput8 du système (processus 32 bits : syswow64). */
        if (!GetSystemDirectoryW(path, MAX_PATH)) return FALSE;
        lstrcatW(path, L"\\dinput8.dll");
        real = LoadLibraryW(path);
        if (!real || real == inst) return FALSE;
        d_create = GetProcAddress(real, "DirectInput8Create");
        d_canunload = GetProcAddress(real, "DllCanUnloadNow");
        d_getclass = GetProcAddress(real, "DllGetClassObject");
        d_register = GetProcAddress(real, "DllRegisterServer");
        d_unregister = GetProcAddress(real, "DllUnregisterServer");
        install_hooks(inst);
    }
    return TRUE;
}
