# Generated by Django 3.0.7 on 2020-08-24 13:10

from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('main', '0002_auto_20200822_2232'),
    ]

    operations = [
        migrations.AlterField(
            model_name='transaction',
            name='reference',
            field=models.CharField(blank=True, default='', max_length=500),
        ),
    ]
