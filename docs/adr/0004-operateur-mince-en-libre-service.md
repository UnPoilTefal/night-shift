# Un opérateur mince en libre-service : le Poste se traduit en objets natifs, la passe tourne dans le namespace de l'équipe

Pour le palier plateforme, une équipe déclare un **Poste** dans son namespace, à la manière d'un `CronWorkflow` d'Argo. Un opérateur le traduit en objets Kubernetes natifs. Chaque déclenchement crée un objet **Passe**. Pour chaque Passe, l'opérateur crée un Job orchestrateur dans le namespace de l'équipe. Ce Job exécute le même binaire `night-shift` que le palier solo : il sélectionne les tickets, pose les Réservations et produit le Digest. Il lance ensuite un Job par ticket, composé d'un conteneur agent sans jeton suivi d'un conteneur de Publication (ADR 0005). L'opérateur ne parle jamais à la forge et ne lit aucun secret d'équipe. Il traduit des objets, impose les garde-fous et fait remonter les statuts.

## Considered Options

- **Un `CronJob` sans opérateur** : c'est ce que garde le palier solo. Écarté pour le palier plateforme, parce qu'il n'offre ni abstraction de domaine (Mission, Source de tickets, Profil d'exécution), ni statut lisible par Passe, ni de point unique où imposer les garde-fous aux équipes.
- **La logique de passe dans le processus du contrôleur** : écarté. Il aurait fallu réécrire le module Passe en réconciliation asynchrone, et surtout le contrôleur central aurait dû lire le jeton de forge de chaque équipe dans chaque namespace.
- **Un contrôleur qui gère lui-même la planification** : écarté au profit d'objets natifs (Job, cron), plus éprouvés.

## Consequences

- **Responsabilité partagée** : l'équipe fournit ses secrets (jeton de forge, accès au modèle) dans son namespace, et le Poste les référence. La plateforme fournit l'opérateur, les Profils d'exécution prédéfinis et les garde-fous.
- **Les garde-fous de l'ADR 0002 sont imposés par l'opérateur au pod de l'agent, quel que soit le Profil d'exécution** : sortie réseau limitée au proxy, aucun jeton de forge, compte de service sans droits. Un profil choisit ce qui tourne, jamais ce qui lui est permis.
- **Toute la logique métier vit dans le binaire `night-shift`**, partagé avec le palier solo. Elle est validée d'abord en `CronJob`, et l'opérateur n'ajoute que la traduction Kubernetes.
- **Déclencheur et Mission sont des unions** (un seul membre renseigné). La v1 ne connaît que le déclenchement par cron, plus la création manuelle d'une Passe, et la seule Mission « tickets ». Le déclenchement par événement et la Mission « incident » pourront venir sans casser l'API, mais le premier remplacera l'ADR 0001 et la seconde demandera son propre ADR de sécurité.
- **Point ouvert** : qui fournit le proxy de sortie, la plateforme ou chaque namespace.
