# Forge Pages

Forge Pages is a GitHub/GitLab Pages pendant for Forge Servers (Forgejo/Gitea) to deploy static webpages.

Unlike other pages services such as the [Codeberg Page Server](https://codeberg.org/Codeberg/pages-server/), static assets do not have to be committed into a separate branch or registry, but can be built and deployed directly from within a workflow. There is also a [suitable action that simplifies the deployment](https://code.leon.wtf/leon/Forge-Pages-Action).

Deployed pages can be secured by enforcing the user permissions configured in the repository, the page was deployed from. This makes this tool a great fit for deploying private pages as well!


## Features

- Deploy static pages using the [Forge Pages Action](https://code.leon.wtf/leon/Forge-Pages-Action) or manually using `tar` and `curl`
- Uses the worflow token (e.g. `${{ forgejo.token }}`) to ensure write permissions to the repository before deployment
- Same URL-layout as with GitHub/GitLab Pages: `https://<owner>.<base-url>/<repo>/*`
- Can protect pages with the Forgejo/Gitea OAuth2 provider


## How does it work?

The Forge Pages Server is a Go application that provides a `POST /deploy` endpoint. Pages must be TAR'ed and GZIP'ed and posted to this endpoint, together with the following query parameters:
- `repo`: Repository slug. Used to construct the URL where the page will be deployed to.
- `access_token`: The workflow token (e.g. `${{ forgejo.token }}`) to verify permissions to deploy a page to the target specified by `repo`.
    - This can also be a PAT, as long as it has the appropriate permissions
- `protect`: If existent, the page will be protected using the Forgejo/Gitea OAuth2 provider. Only users with at least `read`/`pull` permissions can view this page.
    - Alternativly, you can add an empty file called `.protect` to the root of the page to enable protection

When visiting a protected page, you will get redirected to the configured OAuth2 provider, where you must log in. If you have the correct permission, you will get redirected to the page.

Deployments can be deleted using the `DELETE /deploy` endpoint or by uploading an empty page to the same location. The delete endpoint also requires the `repo` and `access_token` parameters.


## Installation

You can use the provided `compose.yml`. Copy the `config.example.yml` to `config.yml` and adjust it for your needs:
- `forge_url`: URL to your Forgejo/Gitea instance. Used to make API calls to verify permissions.
- `pages_url`: Public URL where you intend to deploy the Forge Pages server. **Keey in mind that you need to have a wildcard DNS entry for subdomains on this URL as well!**
- `serve_path`: Base path where pages will be stored and served from. To persist deployments across container restarts, create a bind/volume mount for this path (already contained in the `compose.yml`)
- `oauth.*`: OAuth2 settings as returned by the OAuth2 provider (see [here](https://forgejo.org/docs/latest/user/oauth2-provider/) for how to set up a new OAuth2 application with Forgejo)

This application does not support serving HTTPS directly so you will need to configure a reverse proxy as well.

After setting everything up, you can start Forge Pages with `docker compose up -d`.


## Deploy using the Forge Pages Action

See [here](https://code.leon.wtf/leon/Forge-Pages-Action) for documentation and an example.


## Local development

You can use the CLI flag `-skip_deploy_checks` to skip checking the `access_token` when deploying new pages. This way, you don't need a valid access token.

It is recommended to add the Pages URL and some subdomains you intend to deploy to as static local hosts. See `etc-hosts_TESTING` to see some examples.

Create a basic `config.yml` and start the application with `go run . -skip_deploy_checks`.
