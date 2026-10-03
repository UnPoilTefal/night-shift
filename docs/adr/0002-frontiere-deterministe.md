# La frontière de l'agent est déterministe, pas confiée à sa consigne

Tout ce que l'agent ne doit pas faire est garanti par un mécanisme qui ne dépend pas de son obéissance. Les consignes et règles de permission côté agent ne servent que de première ligne : les classifieurs d'actions ont des faux négatifs, et le contenu d'un ticket peut porter une injection de prompt qu'aucun éditeur ne prétend savoir neutraliser. D'où le principe du « lethal trifecta » : jamais à la fois contenu non fiable, droits d'écriture et accès réseau ou secrets.

Concrètement :
- les **zones interdites** (CI, chaîne de release, configuration de l'agent dont l'Adhésion elle-même) sont protégées par un check CI requis ;
- les **zones sensibles** (par exemple les dépendances) sont signalées par un check, et c'est un humain qui tranche ;
- la **sortie réseau** est fermée par défaut : seuls le DNS et un proxy de sortie à liste blanche de domaines sont joignables, ce qui fonctionne aussi avec un CNI sans règles par domaine ;
- l'agent **ne lit que du contenu d'auteurs de confiance**, filtré par la passe, et un commentaire de tiers postérieur au brief rend le ticket à un humain ;
- l'agent n'ouvre que des **PR en brouillon**, ne merge jamais et ne pousse jamais de tag.

## Consequences

Un repo incapable d'imposer des checks requis ne peut pas adhérer. L'accès à une zone réseau interne n'est accordé que par profil de sortie explicite, quand un ticket réel l'exige.
