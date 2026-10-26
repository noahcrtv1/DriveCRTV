# DriveCRTV

App de Mac para baixar pastas e arquivos públicos do Google Drive em alta velocidade, sem login.
Faz parte da família CRTV de apps.

- Cole o link de uma pasta ou arquivo público do Drive, escolha o que baixar e o destino.
- Download em blocos paralelos, com retomada se cair a conexão.
- Chave de API do Google opcional: lista pastas grandes por completo e confere a integridade (MD5).

## Instalar

Baixe `DriveCRTV-mac.zip` em Releases, arraste `DriveCRTV.app` para Aplicativos e, como o app não é assinado, rode:

```
xattr -cr /Applications/DriveCRTV.app
```

Funciona em Apple Silicon e Intel.

## Compilar

Precisa de Go e Python 3 com Pillow (para o ícone).

```
./packaging/build.sh
```

Gera `dist/DriveCRTV.app` e `dist/DriveCRTV-mac.zip`. Testes: `go test -race .`

## Licença

MIT
