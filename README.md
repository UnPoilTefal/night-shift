# night-shift

Des **passes** planifiées d'agents IA de code qui traitent, sans déclenchement manuel, les **tickets prêts** d'un issue tracker (issues portant le rôle `ready-for-agent` et un brief d'agent autosuffisant). Chaque passe produit des PR en brouillon et un **digest** à relire par un humain.

Le cadre vise trois paliers : un usage solo, une équipe, puis une offre livrée par une équipe platform. Les décisions qui le fondent sont dans [`docs/adr/`](docs/adr/), le vocabulaire dans [`CONTEXT.md`](CONTEXT.md).

Statut : cadrage terminé, implémentation du premier palier à venir.
