# La plateforme fournit le proxy de sortie, et le homelab est un tenant unique du même opérateur

Amende l'ADR 0004 et ferme son point ouvert. Le proxy de sortie est **fourni par la plateforme** et mutualisé entre tenants. La liste d'autorisation d'un pod d'agent dérive de son profil d'exécution (accès au modèle, registres de paquets), et un tenant ne peut pas l'élargir seul. Un **tenant** est le périmètre d'une équipe : son namespace, ses secrets, ses postes et son quota.

Un homelab est une plateforme à un seul tenant : il fait tourner **le même opérateur**, pas un chemin à part. Le `CronJob` du palier solo reste jusqu'à ce que l'opérateur atteigne la parité fonctionnelle, puis il est retiré.

## Considered Options

- **Un proxy par tenant.** Écarté : chaque équipe peut mal le configurer, et un garde-fou que l'équipe surveillée configure n'en est plus un. C'est aussi une source de tickets d'exploitation pour l'équipe plateforme.
- **Deux formes de déploiement durables** (`CronJob` solo et opérateur) ou une pile Docker Compose. Écartées : deux chemins à maintenir, et le homelab cesserait de servir de pilote permanent à l'offre plateforme.

## Consequences

- La chart de la plateforme embarque le proxy ; le passage par une passerelle de modèle (Bedrock, LLM interne) se fera par le profil d'exécution et la liste d'autorisation, sans changement d'architecture.
- Tant que la parité n'est pas atteinte, les évolutions métier restent dans le binaire `night-shift`, partagé par les deux chemins (ADR 0004).
