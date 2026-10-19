# Сторонние компоненты установочного комплекта

warpd запускает эти программы как отдельные процессы. Они не являются собственным кодом warpd, и их лицензии не заменяются лицензией оболочки или документации. Здесь описаны закреплённые версии; обновлять версии и контрольные суммы нужно вместе с проверкой совместимости.

## WarpScout 0.16.0

- Проект: https://github.com/vernette/warpscout
- Релиз и готовые Linux-бинарники: https://github.com/vernette/warpscout/releases/tag/v0.16.0
- Исходники закреплённой версии: https://github.com/vernette/warpscout/tree/v0.16.0
- Архив исходников: https://github.com/vernette/warpscout/archive/refs/tags/v0.16.0.tar.gz
- Лицензия: MIT; её исходный текст включён в комплект как `licenses/warpscout-MIT.txt` (в репозитории: `packaging/licenses/warpscout-MIT.txt`). Сохраняйте исходные уведомления об авторских правах.

| Загружаемый файл | SHA-256 сжатого архива |
| --- | --- |
| `warpscout_0.16.0_linux_arm64.tar.gz` | `261ef88a1ac6eb405ea2d11d80fcce823913ae1faaff9cdc1dff4465c516483d` |
| `warpscout_0.16.0_linux_amd64.tar.gz` | `67a10e3e17b83912a44c72f6e486490c0cbb90d19a51111cec32d5507d6a0f5c` |

## Mihomo 1.19.31

- Проект: https://github.com/MetaCubeX/mihomo
- Релиз и бинарники: https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.31
- Исходники и upstream-рецепты сборки: https://github.com/MetaCubeX/mihomo/tree/v1.19.31
- Архив исходников: https://github.com/MetaCubeX/mihomo/archive/refs/tags/v1.19.31.tar.gz
- Дополнительные исходные зависимости upstream: https://github.com/MetaCubeX/mihomo/releases/download/v1.19.31/vendor.tar.gz
- Опубликованный upstream-комплект toolchain: https://github.com/MetaCubeX/mihomo/releases/download/v1.19.31/toolchain.tar.gz
- Лицензия: GNU GPL v3; исходный текст включён как `licenses/mihomo-GPL-3.0.txt` (в репозитории: `packaging/licenses/mihomo-GPL-3.0.txt`).

Готовые бинарники взяты из указанного upstream-релиза без изменения кода Mihomo. Для соответствующих исходников используйте **тот же** тег и зависимости выше, а не текущую ветку `main`. Сохраняйте лицензию, исходные уведомления и доступ к соответствующим исходникам при дальнейшем распространении. Сведения о сторонних зависимостях самого Mihomo находятся в его upstream-дереве; данная страница не заменяет их лицензии.

| Загружаемый файл | SHA-256 gzip-файла |
| --- | --- |
| `mihomo-linux-arm64-v1.19.31.gz` | `9e0f11afbf38426b8bd88fdc594678f8161c57eccb4e1b77acb12b493904f1d4` |
| `mihomo-linux-amd64-v1-v1.19.31.gz` | `d4304c546c3cddcb6fafd4b4fddb0ba1a95ffa36606fda56d75db2e59ad24114` |

## Relay — llimonix

Спасибо [llimonix](https://github.com/nellimonix) за Relay и проект [warp-config-generator-vercel](https://github.com/nellimonix/warp-config-generator-vercel). warpd использует отдельную клиентскую реализацию для работы с Relay; веб-приложение генератора в комплект не включается.

## Получение и размещение

Сборщик проверяет загруженные файлы по закреплённым SHA-256 до распаковки. Контрольные суммы помогают проверить целостность, но сами по себе не являются цифровой подписью или доказательством доверенного происхождения всего установочного архива.

При обычной установке зависимости принадлежат warpd и размещаются в `/usr/libexec/warpd/`. При явном использовании внешних файлов (`--mihomo`, `--warpscout`) установщик создаёт ссылки; обновление и лицензирование внешней копии остаются у её владельца, а удаление warpd не удаляет цель ссылки.

Сборщик не включает аккаунты Cloudflare, закрытые ключи, токены API, локальные резервные копии или активную конфигурацию роутера. Использование Cloudflare WARP также подчиняется условиям соответствующего сервиса.
