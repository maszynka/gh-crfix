# Rejestr modeli (`internal/registry`)

## Po co to jest

Launcher i kod auto-wyboru backendu w gh-crfix muszą wiedzieć, jakie
aliasy modeli Claude i OpenAI są aktualnie dostępne. Lista ta zmienia
się między wydaniami gh-crfix (nowe modele, wycofane aliasy), więc
zamiast zaszywać ją na stałe w binarce, pobieramy świeżą wersję z
repozytorium. Dzięki temu już wydane binarki same dowiadują się o
nowych modelach bez konieczności rekompilacji.

## Kolejność rozwiązywania (5 kroków)

Funkcja `registry.Fetch` sprawdza źródła po kolei i zwraca pierwsze,
które da się poprawnie zdekodować:

1. **`GH_CRFIX_MODEL_REGISTRY`** — jeśli zmienna środowiskowa jest
   ustawiona, pobieramy JSON-a spod tego URL-a (`Source="env-override"`).
   Override nie zapisuje się do cache'u, żeby nie zaśmiecać domyślnego
   slotu eksperymentalnymi mirrorami.
2. **Świeży cache** — jeśli `~/.cache/gh-crfix/models.json` istnieje i
   ma mtime młodszy niż 1 h, używamy go bez sieci (`Source="cache"`).
3. **HTTP** — pobieramy
   `https://raw.githubusercontent.com/maszynka/gh-crfix/main/registry/models.json`
   (timeout 5 s, tylko 2xx). Sukces nadpisuje cache atomowo
   (tmp + rename) i zwraca `Source="http"`.
4. **Lokalny fallback** — jeśli HTTP padło, czytamy
   `./registry/models.json` względem `RepoRoot` (domyślnie CWD). Ten
   krok ratuje działanie w checkoutcie repo bez sieci
   (`Source="local"`).
5. **Baked-in** — gdy wszystko inne zawiedzie, zwracamy listę aliasów
   zakodowaną w binarce (`Source="baked-in"`). `Fetch` praktycznie
   nigdy nie zwraca błędu, bo ten krok zawsze się udaje.

Błędy sieci, statusy != 2xx, malformed JSON i puste listy aliasów są
traktowane jako miss i powodują przejście do kolejnego kroku.

## Nadpisanie URL-a

```sh
export GH_CRFIX_MODEL_REGISTRY=https://example.com/my-mirror/models.json
gh crfix ...
```

Przydatne do testów na staging-owym forku, przy pracy offline z
wewnętrznym mirrorem, albo do wymuszenia starszej wersji rejestru.

## Cache

- Lokalizacja: `$XDG_CACHE_HOME/gh-crfix/models.json` albo
  `~/.cache/gh-crfix/models.json`.
- TTL: 1 h (mtime pliku).
- Zapis: atomowy (tmp + rename) — równolegle uruchomione procesy
  nigdy nie widzą połowicznie zapisanego pliku.
- Czyszczenie: bezpiecznie można usunąć ręcznie; następne uruchomienie
  pobierze świeży JSON.

## Utrzymanie `registry/models.json` w repo

Plik wersjonowany w repo jest fallbackiem dla użytkowników offline i
źródłem prawdy dla endpointu HTTP (jest serwowany z `main` przez
`raw.githubusercontent.com`). Aktualizuje go skrypt
[`registry/update.sh`](../registry/update.sh), który pyta providerów o
aktualną listę modeli i zapisuje JSON-a. CI uruchamia ten skrypt
okresowo i commituje wynik (commity `chore(registry): update model
lists`), więc w praktyce wystarczy tylko mergować PR-y od bota.

Ręcznie można odświeżyć tak:

```sh
./registry/update.sh
git add registry/models.json
git commit -m "chore(registry): update model lists"
```
