# L'agent ne détient aucun droit d'écriture sur la forge : il produit un patch, la Publication le pousse

Le conteneur qui exécute le modèle reçoit un brief filtré et un clone du dépôt cible. Il commite localement, puis rend sa série de commits sous forme de patchs (`git format-patch base..HEAD`). Il n'a aucun jeton de forge. Une étape de **Publication**, déterministe et sans modèle, applique la série avec `git am --3way`, ce qui conserve messages et découpage en commits, ajoute le trailer d'attribution, pousse la branche `agent/` et ouvre la PR en brouillon. C'est la seule étape qui détient un droit d'écriture. L'ADR 0002 ne fait que restreindre la jambe « droits d'écriture » du lethal trifecta. Ici, on la retire physiquement du périmètre de l'agent : même manipulé par le contenu d'un ticket, il ne peut produire qu'un mauvais patch, que les checks requis et un humain rejettent.

## Considered Options

- **L'agent pousse lui-même avec un jeton restreint**, comme le prévoyait la première version de la spec du palier solo. Cette option a été écartée : le jeton est à portée d'une injection de prompt. Le restreindre aux repos adhérents limite les dégâts, sans les empêcher.

- **Transmettre un diff unique** (`git diff` + `git apply`) : écarté, car il aplatit l'historique et perd les messages de commit. Le canal `format-patch`/`am` reprend celui des sandbox isolées de `mattpocock/sandcastle` (son ADR 0017), qui fait la même séparation entre le sandbox et l'hôte.

## Consequences

- La séparation passe par une **frontière de conteneur**, pas par une frontière de processus. Un sous-processus lancé sous le même utilisateur peut lire l'environnement et la mémoire de son parent. Il ne suffit donc pas de lancer `claude` avec un environnement vidé depuis un processus qui détient le jeton.
- L'agent peut quand même itérer sur la CI : la passe le relance avec le retour des checks en échec, et la Publication pousse la nouvelle série sur la même branche. À chaque relance, l'agent repart d'un clone neuf de la branche `agent/` déjà publiée. Le décalage de base que provoque `git am` en réécrivant les SHA ne peut donc pas se produire. Sandcastle, qui réutilise un même sandbox, doit le corriger par une ref dédiée (son ADR 0017).
- L'Identité de service est portée par la passe (Réservation) et par la Publication, jamais par l'agent.
